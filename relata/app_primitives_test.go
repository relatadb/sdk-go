package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAppPrimitives_ClaimCompleteAndListShapes(t *testing.T) {
	type call struct {
		path, query string
		body        map[string]any
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		calls = append(calls, call{r.URL.Path, r.URL.RawQuery, body})
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/queues/q/claim" {
			fmt.Fprint(w, `{"jobs":[{"jobId":"j1","fencingToken":7}]}`)
			return
		}
		fmt.Fprint(w, `{"state":"done","jobs":[]}`)
	}))
	defer srv.Close()

	a := NewAppPrimitivesClient(newTestClient(srv, nil))
	ctx := context.Background()
	jobs, err := a.Claim(ctx, "q", "w1", 1000, 2)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim: %v %v", jobs, err)
	}
	if _, err := a.Complete(ctx, "j1", 7, map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ListJobs(ctx, "q", "dead", 5); err != nil {
		t.Fatal(err)
	}
	if calls[0].body["workerId"] != "w1" || calls[0].body["leaseMs"] != float64(1000) {
		t.Fatalf("claim body: %v", calls[0].body)
	}
	if calls[1].path != "/jobs/j1/complete" || calls[1].body["fencingToken"] != float64(7) {
		t.Fatalf("complete call: %+v", calls[1])
	}
	if calls[2].path != "/queues/q/jobs" || calls[2].query != "limit=5&state=dead" {
		t.Fatalf("list call: %+v", calls[2])
	}
}

func TestAppPrimitives_SequenceNextOmitsUnsetFields(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"start":1,"end":1}`)
	}))
	defer srv.Close()
	a := NewAppPrimitivesClient(newTestClient(srv, nil))
	if _, err := a.SequenceNext(context.Background(), "s", 0, ""); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty body, got %v", got)
	}
}
