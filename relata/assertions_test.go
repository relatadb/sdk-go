package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAssertionsRoutesAndBodies(t *testing.T) {
	var seen [][]any
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped" {
			t.Errorf("auth not inherited: %v", r.Header)
		}
		var b map[string]any
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&b)
		}
		seen = append(seen, []any{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, b})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	a := NewAssertionsClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	ctx := context.Background()
	claim := map[string]any{"predicate": "city", "value": "Rome"}
	def := map[string]any{"precedence": []any{"state"}}
	nine := int64(9)
	calls := []func() error{
		func() error { _, e := a.Record(ctx, claim); return e },
		func() error { _, e := a.List(ctx, "", "", "", "", 0); return e },
		func() error { _, e := a.List(ctx, "Person", "p 1", "city", "confirmed", 5); return e },
		func() error { _, e := a.Get(ctx, "asr_1"); return e },
		func() error { _, e := a.Confirm(ctx, "asr_1", "verified"); return e },
		func() error { _, e := a.Reject(ctx, "asr/2", "stale"); return e },
		func() error { _, e := a.Revoke(ctx, "asr_1", "error"); return e },
		func() error { _, e := a.PublishPolicy(ctx, "trust", def, 2); return e },
		func() error { _, e := a.GetPolicy(ctx, "trust", 0); return e },
		func() error { _, e := a.GetPolicy(ctx, "trust", 3); return e },
		func() error { _, e := a.Resolve(ctx, "Person", "p1", "city", 5, ResolveOptions{}); return e },
		func() error {
			_, e := a.Resolve(ctx, "Person", "p1", "city", 5, ResolveOptions{RecordedAsOf: &nine, Policy: "trust", PolicyVersion: 2})
			return e
		},
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	want := [][]any{
		{"POST", "/v1/assertions", "", claim},
		{"GET", "/v1/assertions", "", map[string]any(nil)},
		{"GET", "/v1/assertions", "limit=5&predicate=city&state=confirmed&subject_id=p+1&subject_type=Person", map[string]any(nil)},
		{"GET", "/v1/assertions/asr_1", "", map[string]any(nil)},
		{"POST", "/v1/assertions/asr_1/decision", "", map[string]any{"decision": "confirm", "reason": "verified"}},
		{"POST", "/v1/assertions/asr%2F2/decision", "", map[string]any{"decision": "reject", "reason": "stale"}},
		{"POST", "/v1/assertions/asr_1/revoke", "", map[string]any{"reason": "error"}},
		{"PUT", "/v1/assertions/policies/trust", "", map[string]any{"expected_version": float64(2), "definition": def}},
		{"GET", "/v1/assertions/policies/trust", "", map[string]any(nil)},
		{"GET", "/v1/assertions/policies/trust", "version=3", map[string]any(nil)},
		{"GET", "/v1/assertions/resolve", "predicate=city&subject_id=p1&subject_type=Person&valid_at=5", map[string]any(nil)},
		{"GET", "/v1/assertions/resolve", "policy=trust&policy_version=2&predicate=city&recorded_as_of=9&subject_id=p1&subject_type=Person&valid_at=5", map[string]any(nil)},
	}
	if len(seen) != len(want) {
		t.Fatalf("got %d requests, want %d: %v", len(seen), len(want), seen)
	}
	for i := range want {
		got, _ := json.Marshal(seen[i])
		exp, _ := json.Marshal(want[i])
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("request %d:\n got %s\nwant %s", i, got, exp)
		}
	}
	status = http.StatusConflict
	if _, err := a.Revoke(ctx, "asr_1", "x"); err == nil {
		t.Fatal("a 409 must surface as an error")
	}
}
