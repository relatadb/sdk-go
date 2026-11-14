package relata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"
)

// #5689 (owner decision D06): every POST /v1/ingest/bulk gets an automatic
// Idempotency-Key, reused by the client's retries (network errors, 502, 504;
// never a 503 from Relata); an explicit key is never replaced, and the queued
// /v1/ingest door takes no key and is never retried.

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type bulkRecorder struct {
	mu      sync.Mutex
	paths   []string
	keys    []string
	replies []int // per-request status; 0 = drop the connection
}

func (b *bulkRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.paths = append(b.paths, r.URL.Path)
		b.keys = append(b.keys, r.Header.Get("Idempotency-Key"))
		status := http.StatusOK
		if len(b.replies) > 0 {
			status, b.replies = b.replies[0], b.replies[1:]
		}
		b.mu.Unlock()
		if status == 0 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, `{"inserted":1,"errors":[],"task_id":"itsk_1","rows_queued":1}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func bulkClient(srv *httptest.Server, retries int) *IngestClient {
	return NewIngestClient(newTestClient(srv, &ClientOptions{
		MaxRetries:   retries,
		RetryBackoff: time.Millisecond,
	}))
}

var keylessRows = []map[string]any{{"note": "keyless"}}

func TestBulkGetsAFreshAutomaticKeyPerCall(t *testing.T) {
	rec := &bulkRecorder{}
	ic := bulkClient(rec.server(t), 0)
	for i := 0; i < 2; i++ {
		if _, err := ic.Bulk(context.Background(), "Note", keylessRows, &BulkOptions{OnConflict: "upsert"}); err != nil {
			t.Fatalf("Bulk: %v", err)
		}
	}
	if rec.paths[0] != "/v1/ingest/bulk" || !uuidPattern.MatchString(rec.keys[0]) {
		t.Fatalf("path=%q key=%q", rec.paths[0], rec.keys[0])
	}
	if rec.keys[0] == rec.keys[1] {
		t.Fatalf("two calls shared key %q", rec.keys[0])
	}
}

func TestBulkExplicitKeyIsNeverReplaced(t *testing.T) {
	rec := &bulkRecorder{}
	ic := bulkClient(rec.server(t), 0)
	if _, err := ic.Bulk(context.Background(), "Note", keylessRows, &BulkOptions{IdempotencyKey: "batch-7"}); err != nil {
		t.Fatalf("Bulk: %v", err)
	}
	if rec.keys[0] != "batch-7" {
		t.Fatalf("key = %q", rec.keys[0])
	}
}

func TestBulkRetriesReuseTheSameKey(t *testing.T) {
	rec := &bulkRecorder{replies: []int{502, 0, 504}}
	ic := bulkClient(rec.server(t), 3)
	if _, err := ic.Bulk(context.Background(), "Note", keylessRows, &BulkOptions{OnConflict: "upsert"}); err != nil {
		t.Fatalf("Bulk: %v", err)
	}
	if len(rec.keys) < 4 {
		t.Fatalf("attempts = %d, want 4", len(rec.keys))
	}
	for _, k := range rec.keys {
		if k != rec.keys[0] || !uuidPattern.MatchString(k) {
			t.Fatalf("keys differ across retries: %v", rec.keys)
		}
	}
}

func TestBulkRelata503IsNotRetried(t *testing.T) {
	rec := &bulkRecorder{replies: []int{503, 200}}
	ic := bulkClient(rec.server(t), 2)
	if _, err := ic.Bulk(context.Background(), "Note", keylessRows, &BulkOptions{OnConflict: "upsert"}); err == nil {
		t.Fatal("want the 503 error")
	}
	if len(rec.keys) != 1 {
		t.Fatalf("attempts = %d, want 1", len(rec.keys))
	}
}

func TestQueuedIngestDoorGetsNoKeyAndNoRetry(t *testing.T) {
	rec := &bulkRecorder{replies: []int{502, 200}}
	ic := bulkClient(rec.server(t), 2)
	if _, err := ic.Bulk(context.Background(), "Note", keylessRows, nil); err == nil {
		t.Fatal("want the 502 error")
	}
	if len(rec.keys) != 1 || rec.paths[0] != "/v1/ingest" || rec.keys[0] != "" {
		t.Fatalf("paths=%v keys=%v", rec.paths, rec.keys)
	}
}

func TestIngestDocumentIsKeyedAndRetried(t *testing.T) {
	rec := &bulkRecorder{replies: []int{504, 200}}
	c := newTestClient(rec.server(t), &ClientOptions{MaxRetries: 1, RetryBackoff: time.Millisecond})
	objects := []map[string]any{{"_type": "DocumentSource", "_pk": "d1"}}
	if _, err := c.IngestDocument(context.Background(), objects, ""); err != nil {
		t.Fatalf("IngestDocument: %v", err)
	}
	if len(rec.keys) != 2 || rec.keys[0] != rec.keys[1] || !uuidPattern.MatchString(rec.keys[0]) {
		t.Fatalf("keys = %v", rec.keys)
	}
}
