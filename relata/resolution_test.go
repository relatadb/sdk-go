package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestResolutionRoutesAndBodies(t *testing.T) {
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
	o := NewResolutionClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	ctx := context.Background()
	d := map[string]any{"entity_type": "Person"}
	props := []map[string]any{{"rule": "m", "a": "x", "b": "y", "score": 0.9}}
	pairs := []map[string]any{{"a": "x", "b": "y", "same": true}}
	calls := []func() error{
		func() error { _, e := o.ListRulesets(ctx); return e },
		func() error { _, e := o.GetRuleset(ctx, "people", 0); return e },
		func() error { _, e := o.GetRuleset(ctx, "people", 2); return e },
		func() error { _, e := o.PublishRuleset(ctx, "people", d, 1); return e },
		func() error { _, e := o.Run(ctx, "people", nil); return e },
		func() error { _, e := o.Run(ctx, "people", props); return e },
		func() error { _, e := o.Evaluate(ctx, "people", pairs, nil); return e },
		func() error { _, e := o.ListCandidates(ctx, "", "", 0); return e },
		func() error { _, e := o.ListCandidates(ctx, "people", "pending", 5); return e },
		func() error { _, e := o.GetCandidate(ctx, "cand_1"); return e },
		func() error { _, e := o.Approve(ctx, "cand_1", "same mailbox"); return e },
		func() error { _, e := o.Reject(ctx, "cand_2", "different people"); return e },
		func() error { _, e := o.Unmerge(ctx, "cand_1", "wrong"); return e },
		func() error { _, e := o.Entity(ctx, "ent_1"); return e },
		func() error { _, e := o.Lookup(ctx, "Person", "r 1"); return e },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	nb := map[string]any(nil)
	jprops := []any{map[string]any{"rule": "m", "a": "x", "b": "y", "score": 0.9}}
	jpairs := []any{map[string]any{"a": "x", "b": "y", "same": true}}
	want := [][]any{
		{"GET", "/resolution/rulesets", "", nb},
		{"GET", "/resolution/rulesets/people", "", nb},
		{"GET", "/resolution/rulesets/people", "version=2", nb},
		{"PUT", "/resolution/rulesets/people", "", map[string]any{"expected_version": float64(1), "definition": d}},
		{"POST", "/resolution/rulesets/people/run", "", map[string]any{}},
		{"POST", "/resolution/rulesets/people/run", "", map[string]any{"proposals": jprops}},
		{"POST", "/resolution/rulesets/people/evaluate", "", map[string]any{"labeled": jpairs}},
		{"GET", "/resolution/candidates", "", nb},
		{"GET", "/resolution/candidates", "limit=5&ruleset=people&state=pending", nb},
		{"GET", "/resolution/candidates/cand_1", "", nb},
		{"POST", "/resolution/candidates/cand_1/decision", "", map[string]any{"decision": "approve", "reason": "same mailbox"}},
		{"POST", "/resolution/candidates/cand_2/decision", "", map[string]any{"decision": "reject", "reason": "different people"}},
		{"POST", "/resolution/candidates/cand_1/decision", "", map[string]any{"decision": "unmerge", "reason": "wrong"}},
		{"GET", "/resolution/entities/ent_1", "", nb},
		{"GET", "/resolution/lookup", "id=r+1&type=Person", nb},
	}
	// JSON round trip so numbers and nested maps compare like the server saw them.
	norm := func(v any) any { b, _ := json.Marshal(v); var o any; _ = json.Unmarshal(b, &o); return o }
	if !reflect.DeepEqual(norm(seen), norm(want)) {
		t.Fatalf("calls differ:\n got %v\nwant %v", norm(seen), norm(want))
	}
}

func TestResolutionNamesAreQuotedAndConflictsReturned(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusPreconditionFailed)
		w.Write([]byte(`{"code":"resolution-version-conflict","status":412,"title":"x","detail":"d"}`))
	}))
	defer srv.Close()
	o := NewResolutionClient(newTestClient(srv, nil))
	if _, err := o.PublishRuleset(context.Background(), "a/b c", map[string]any{}, 0); err == nil {
		t.Fatal("a 412 must be returned as an error")
	}
	if path != "/resolution/rulesets/a%2Fb%20c" {
		t.Fatalf("path not quoted: %s", path)
	}
}
