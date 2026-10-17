package relata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const queryStreamStart = `{"_meta":{"query_id":"query-123","started_at":1,"columns":["id","_meta"]}}` + "\n"
const queryStreamEndOne = `{"_meta":{"processing_time_ms":3,"row_count":1,"truncated":false}}` + "\n"

type queryStreamTrackedBody struct {
	io.Reader
	closes int
}

func (b *queryStreamTrackedBody) Close() error { b.closes++; return nil }

func TestQueryRowsCompletionPreservesMetaColumnsAndClosesBody(t *testing.T) {
	for _, row := range []string{`{"id":1,"_meta":"user value"}`, `{"_meta":null}`, `{"_meta":{"user":"value"}}`, `{"_meta":4}`} {
		t.Run(row, func(t *testing.T) {
			body := &queryStreamTrackedBody{Reader: strings.NewReader(queryStreamStart + row + "\n" + queryStreamEndOne)}
			it := newRowIterator(body, 0)
			if !it.Next() || it.Row() == nil {
				t.Fatalf("row missing: %v", it.Err())
			}
			if it.Next() || it.Err() != nil {
				t.Fatalf("completion: %v", it.Err())
			}
			if body.closes != 1 {
				t.Fatalf("closes = %d", body.closes)
			}
			_ = it.Close()
			_ = it.Close()
			if body.closes != 1 {
				t.Fatalf("Close is not idempotent: %d", body.closes)
			}
		})
	}
}

func TestQueryRowsIncompleteFramesAreTypedAndNeverRestart(t *testing.T) {
	row := `{"id":1}` + "\n"
	cases := map[string]string{
		"missing terminal":   queryStreamStart + row,
		"truncated":          queryStreamStart + row + `{"_meta":{"processing_time_ms":1,"row_count":1,"truncated":true}}`,
		"error":              queryStreamStart + row + `{"_meta":{"processing_time_ms":1,"row_count":1,"truncated":false,"error":"redacted server detail"}}`,
		"count mismatch":     queryStreamStart + row + `{"_meta":{"processing_time_ms":1,"row_count":2,"truncated":false}}`,
		"malformed terminal": queryStreamStart + row + `{"_meta":{"processing_time_ms":1,"row_count":null,"truncated":false}}`,
		"duplicate leading":  queryStreamStart + row + queryStreamStart,
		"duplicate terminal": queryStreamStart + row + queryStreamEndOne + queryStreamEndOne,
		"row after terminal": queryStreamStart + row + queryStreamEndOne + row,
		"malformed JSON":     queryStreamStart + row + "{",
		"non object":         queryStreamStart + row + "[]",
		"invalid UTF8":       queryStreamStart + row + "{\"x\":\"\xff\"}",
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; _, _ = io.WriteString(w, wire) }))
			defer srv.Close()
			c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
			it, err := NewStreamingClient(c).QueryRows(context.Background(), "SELECT id FROM Person", nil)
			if err != nil {
				t.Fatal(err)
			}
			got := 0
			for it.Next() {
				got++
			}
			var incomplete *IncompleteQueryStreamError
			if !errors.As(it.Err(), &incomplete) {
				t.Fatalf("expected typed error, got %v", it.Err())
			}
			if incomplete.QueryID != "query-123" || incomplete.RowsReceived != 1 || got != 1 {
				t.Fatalf("lost progress context: %+v, rows=%d", incomplete, got)
			}
			if requests != 1 {
				t.Fatalf("retried partial stream: %d", requests)
			}
			if strings.Contains(it.Err().Error(), "redacted server detail") {
				t.Fatal("server payload leaked in error")
			}
			_ = it.Close()
		})
	}
}

func TestQueryRowsRequiresLeadingMetadataAndClosesOnFailureOrCancellation(t *testing.T) {
	for _, wire := range []string{"", `{"id":1}`, `null`, `{"_meta":{"query_id":"q","started_at":1,"columns":null}}`} {
		body := &queryStreamTrackedBody{Reader: strings.NewReader(wire)}
		it := newRowIterator(body, 0)
		if it.Next() {
			t.Fatal("yielded before leading metadata")
		}
		var incomplete *IncompleteQueryStreamError
		if !errors.As(it.Err(), &incomplete) || incomplete.RowsReceived != 0 {
			t.Fatalf("error=%v", it.Err())
		}
		_ = it.Close()
		if body.closes != 1 {
			t.Fatalf("failure closes=%d", body.closes)
		}
	}
	body := &queryStreamTrackedBody{Reader: strings.NewReader(queryStreamStart + "{\"id\":1}\n")}
	it := newRowIterator(body, 0)
	if !it.Next() {
		t.Fatal(it.Err())
	}
	_ = it.Close()
	_ = it.Close()
	if it.Next() || it.Err() != nil || body.closes != 1 {
		t.Fatalf("caller cancellation: err=%v closes=%d", it.Err(), body.closes)
	}
}

func TestQueryRowsFrameCapUsesConfiguredResponseLimitAndAllowsWideRows(t *testing.T) {
	row := fmt.Sprintf("{\"text\":%q}\n", strings.Repeat("x", 100*1024))
	for _, test := range []struct {
		limit     int64
		wantError bool
	}{{0, false}, {128 * 1024, false}, {80 * 1024, true}} {
		body := &queryStreamTrackedBody{Reader: strings.NewReader(queryStreamStart + row + queryStreamEndOne)}
		it := newRowIterator(body, test.limit)
		got := 0
		for it.Next() {
			got++
		}
		if (it.Err() != nil) != test.wantError {
			t.Fatalf("limit=%d rows=%d err=%v", test.limit, got, it.Err())
		}
		if body.closes != 1 {
			t.Fatalf("limit=%d closes=%d", test.limit, body.closes)
		}
		if !test.wantError && got != 1 {
			t.Fatalf("wide row missing: %d", got)
		}
	}
}

type queryStreamBrokenReader struct{ remaining string }

func (r *queryStreamBrokenReader) Read(p []byte) (int, error) {
	if r.remaining != "" {
		n := copy(p, r.remaining)
		r.remaining = r.remaining[n:]
		return n, nil
	}
	return 0, io.ErrUnexpectedEOF
}

func TestQueryRowsTransportFailureClosesBodyWithPartialContext(t *testing.T) {
	body := &queryStreamTrackedBody{Reader: &queryStreamBrokenReader{remaining: queryStreamStart + "{\"id\":1}\n"}}
	it := newRowIterator(body, 0)
	if !it.Next() {
		t.Fatal(it.Err())
	}
	if it.Next() {
		t.Fatal("unexpected extra row")
	}
	var incomplete *IncompleteQueryStreamError
	if !errors.As(it.Err(), &incomplete) || incomplete.QueryID != "query-123" || incomplete.RowsReceived != 1 {
		t.Fatalf("error=%v", it.Err())
	}
	if body.closes != 1 {
		t.Fatalf("closes=%d", body.closes)
	}
}

func TestQueryRowsEmptyAnswerNeedsConfirmedZeroCount(t *testing.T) {
	body := &queryStreamTrackedBody{Reader: strings.NewReader(queryStreamStart + `{"_meta":{"processing_time_ms":0,"row_count":0,"truncated":false}}`)}
	it := newRowIterator(body, 0)
	if it.Next() || it.Err() != nil || body.closes != 1 {
		t.Fatalf("empty completion: err=%v closes=%d", it.Err(), body.closes)
	}
}

func TestQueryRowsMetadataAllowsExtensionsWithoutSkippingUnrecognizedData(t *testing.T) {
	wire := `{"_meta":{"query_id":"q-extended","started_at":1,"columns":["_meta"],"future_schema":"v2"}}` + "\n" +
		`{"_meta":{"future_row":"ordinary data"}}` + "\n" +
		`{"_meta":{"processing_time_ms":0,"row_count":1,"truncated":false,"future_receipt":{"accepted":true}}}`
	body := &queryStreamTrackedBody{Reader: strings.NewReader(wire)}
	it := newRowIterator(body, 0)
	if !it.Next() {
		t.Fatal(it.Err())
	}
	value, ok := it.Row()["_meta"].(map[string]any)
	if !ok || value["future_row"] != "ordinary data" {
		t.Fatalf("row=%v", it.Row())
	}
	if it.Next() || it.Err() != nil || body.closes != 1 {
		t.Fatalf("extension completion: err=%v closes=%d", it.Err(), body.closes)
	}
}
