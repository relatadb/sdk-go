package relata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// #5688 — document and CDR SDK batches under server overload. Same contract as
// sdks/python/tests/test_ingest_overload_5688.py and
// sdks/typescript/src/ingest-overload.test.ts: a 429/503 shed surfaces as the
// typed error carrying the server delay, the batch POST is sent exactly once,
// the caller's retry of the same stable-key batch is byte-identical, an
// accepted-then-uncertain 503 hands back the task id, and the SDK-side
// invalid-row report survives the retry.

// shedServer answers the first len(script) batch POSTs with the scripted
// problem responses, then with ok. It records every batch body.
type shedServer struct {
	mu     sync.Mutex
	bodies []string
	script []func(http.ResponseWriter)
	ok     string
}

func (s *shedServer) handler(batchPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != batchPath {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"task_id":"task-9","status":"partial","rows_total":2,"rows_written":1,"rows_dropped":1,"drop_reasons":["graph edge target missing"],"_relata_write_token":null}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(b))
		var shed func(http.ResponseWriter)
		if len(s.script) > 0 {
			shed, s.script = s.script[0], s.script[1:]
		}
		s.mu.Unlock()
		if shed != nil {
			shed(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, s.ok)
	}
}

func (s *shedServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func problemShed(status int, retryAfter, extra string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"detail":"overloaded","code":"ingest-overloaded","retryable":true%s}`, extra)
	}
}

func asRelataError(t *testing.T, err error) *RelataError {
	t.Helper()
	var re *RelataError
	if !errors.As(err, &re) {
		t.Fatalf("error %v is not a *RelataError", err)
	}
	return re
}

func TestIngestDocumentOverloadIsSurfacedOnceAndRetriedByTheCaller(t *testing.T) {
	s := &shedServer{
		script: []func(http.ResponseWriter){problemShed(429, "3", ""), problemShed(503, "5", "")},
		ok:     `{"task_id":"task-9","rows_queued":2,"rows_rejected":0,"errors":[],"queue_depth":2}`,
	}
	srv := httptest.NewServer(s.handler("/v1/ingest/bulk"))
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	rows := []map[string]any{
		{"_type": "DocumentSource", "_pk": "doc-1", "uri": "file://report.pdf"},
		{"_type": "DocumentChunk", "_pk": "chunk-1", "source_id": "doc-1", "text": "hello"},
	}

	_, err := c.IngestDocument(context.Background(), rows, "")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429 err = %v, want ErrRateLimited", err)
	}
	if got := asRelataError(t, err).RetryAfter; got != 3*time.Second {
		t.Fatalf("429 RetryAfter = %v, want 3s", got)
	}
	if s.count() != 1 {
		t.Fatalf("a 429 must not be replayed by the transport, saw %d POSTs", s.count())
	}

	_, err = c.IngestDocument(context.Background(), rows, "")
	if !errors.Is(err, ErrServerError) {
		t.Fatalf("503 err = %v, want ErrServerError", err)
	}
	if got := asRelataError(t, err).RetryAfter; got != 5*time.Second {
		t.Fatalf("503 RetryAfter = %v, want 5s", got)
	}
	if s.count() != 2 {
		t.Fatalf("a 503 must not be replayed by the transport, saw %d POSTs", s.count())
	}

	receipt, err := c.IngestDocument(context.Background(), rows, "")
	if err != nil {
		t.Fatalf("retry after overload cleared: %v", err)
	}
	if receipt.TaskID != "task-9" || receipt.RowsQueued != 2 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if s.count() != 3 || s.bodies[0] != s.bodies[1] || s.bodies[1] != s.bodies[2] {
		t.Fatalf("caller retries must resend the identical stable-key batch: %q", s.bodies)
	}
	status, err := c.IngestDocumentStatus(context.Background(), receipt.TaskID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Status != "partial" || status.RowsWritten != 1 || status.RowsDropped != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestIngestDocumentAcceptedThenUncertain503HandsBackTheTask(t *testing.T) {
	s := &shedServer{
		script: []func(http.ResponseWriter){
			problemShed(503, "2", `,"task_id":"task-uncertain","accepted":true,"receipt_status":"uncertain"`),
		},
	}
	srv := httptest.NewServer(s.handler("/v1/ingest/bulk"))
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	_, err := c.IngestDocument(context.Background(), []map[string]any{{"_type": "DocumentSource", "_pk": "doc-1"}}, "")
	re := asRelataError(t, err)
	if re.TaskID != "task-uncertain" || re.Accepted == nil || !*re.Accepted ||
		re.ReceiptStatus != "uncertain" || re.RetryAfter != 2*time.Second {
		t.Fatalf("uncertain 503 = %+v", re)
	}
	if s.count() != 1 {
		t.Fatalf("an uncertain 503 must not be replayed, saw %d POSTs", s.count())
	}
}

func TestIngestCDROverloadKeepsRowReportAndReplaysNothing(t *testing.T) {
	s := &shedServer{
		script: []func(http.ResponseWriter){problemShed(429, "1", ""), problemShed(503, "4", "")},
		ok:     `{"rows_queued":2,"rows_rejected":0,"errors":[],"task_id":"itsk_cdr"}`,
	}
	srv := httptest.NewServer(s.handler("/v1/ingest"))
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "finint"})
	ic := NewIngestClient(c)
	// Explicit call_start: a row without one is stamped with the SDK wall clock
	// at normalization time, so a retry would not be byte-identical.
	rows := []map[string]any{
		{"caller": "911234567890", "callee": "919876543210", "duration": 120, "tower": "CELL-042", "type": "voice", "_pk": "cdr-1", "source_file_id": 42, "call_start": 1700000000},
		{"caller": "not a number", "callee": "922222222222"},
		{"caller": "911111111111", "callee": "922222222222", "duration": 5, "tower": "CELL-001", "type": "sms", "_pk": "cdr-2", "source_file_id": 43, "call_start": 1700000001},
	}

	_, err := ic.IngestCDR(context.Background(), rows, nil)
	if !errors.Is(err, ErrRateLimited) || asRelataError(t, err).RetryAfter != time.Second || s.count() != 1 {
		t.Fatalf("429: err=%v posts=%d", err, s.count())
	}
	_, err = ic.IngestCDR(context.Background(), rows, nil)
	if !errors.Is(err, ErrServerError) || asRelataError(t, err).RetryAfter != 4*time.Second || s.count() != 2 {
		t.Fatalf("503: err=%v posts=%d", err, s.count())
	}

	out, err := ic.IngestCDR(context.Background(), rows, nil)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if s.count() != 3 || s.bodies[0] != s.bodies[1] || s.bodies[1] != s.bodies[2] {
		t.Fatalf("retries must be byte-identical: %q", s.bodies)
	}
	if out["task_id"] != "itsk_cdr" {
		t.Fatalf("receipt = %v", out)
	}
	if rejected, _ := out["rows_rejected"].(float64); rejected != 1 {
		t.Fatalf("rows_rejected = %v, want the SDK-side invalid row to survive the retry", out["rows_rejected"])
	}
	if errs, _ := out["errors"].([]any); len(errs) != 1 {
		t.Fatalf("errors = %v", out["errors"])
	}
}
