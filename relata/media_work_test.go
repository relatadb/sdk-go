package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestMediaWorkRoutesAndBodies(t *testing.T) {
	var seen [][]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&b)
		}
		seen = append(seen, []any{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, b})
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[{"work_id":"w/1","claim_id":7}]}`))
	}))
	defer srv.Close()
	m := NewMediaWorkClient(newTestClient(srv, &ClientOptions{BearerToken: "s"}))
	ctx := context.Background()
	out, err := m.Claim(ctx, "etl-1", &MediaWorkClaimOptions{MaxItems: 2, LeaseMs: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if items, _ := out["items"].([]any); len(items) != 1 {
		t.Fatalf("items: %v", out)
	}
	for _, f := range []func() error{
		func() error { _, e := m.List(ctx, MediaWorkQueued, 3); return e },
		func() error { _, e := m.List(ctx, "", 0); return e },
		func() error { _, e := m.Get(ctx, "w/1"); return e },
		func() error { _, e := m.Heartbeat(ctx, "w/1", 7, 9000); return e },
		func() error { _, e := m.Complete(ctx, "w/1", 7, "v1", 2, 5); return e },
		func() error { _, e := m.Fail(ctx, "w/1", 7, MediaWorkPermanent, "bad", "corrupt"); return e },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	n := func(v float64) any { return v }
	want := [][]any{
		{"POST", "/media/work/claim", "", map[string]any{"claimant": "etl-1", "max_items": n(2), "lease_ms": n(5000)}},
		{"GET", "/media/work", "limit=3&state=queued", map[string]any(nil)},
		{"GET", "/media/work", "", map[string]any(nil)},
		{"GET", "/media/work/w%2F1", "", map[string]any(nil)},
		{"POST", "/media/work/w%2F1/heartbeat", "", map[string]any{"claim_id": n(7), "lease_ms": n(9000)}},
		{"POST", "/media/work/w%2F1/complete", "", map[string]any{"claim_id": n(7), "producer_version": "v1", "batches": n(2), "children": n(5)}},
		{"POST", "/media/work/w%2F1/fail", "", map[string]any{"claim_id": n(7), "class": "permanent", "code": "bad", "detail": "corrupt"}},
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("got %#v\nwant %#v", seen, want)
	}
	if _, err := m.List(ctx, "bogus", 0); err == nil {
		t.Fatal("expected invalid state error")
	}
	if _, err := m.Fail(ctx, "w", 1, "sometimes", "", ""); err == nil {
		t.Fatal("expected invalid class error")
	}
}

func TestBulkWriteBackSendsClaimHeader(t *testing.T) {
	var path, claim string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, claim = r.URL.Path, r.Header.Get("X-Relata-Claim")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"inserted":1}`))
	}))
	defer srv.Close()
	ing := NewIngestClient(newTestClient(srv, nil))
	_, err := ing.Bulk(context.Background(), "MediaSegment", []map[string]any{{"_pk": "a"}}, &BulkOptions{
		Durability:       "sync",
		IdempotencyKey:   "media/w/v1/1",
		SourceCheckpoint: &SourceCheckpoint{Source: "openetl/media/w", Position: "1"},
		MediaWorkClaim:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/ingest/bulk" || claim != "7" {
		t.Fatalf("path=%q claim=%q", path, claim)
	}
	if h := MediaWorkClaimHeaders(7); h["X-Relata-Claim"] != "7" {
		t.Fatalf("headers %v", h)
	}
}
