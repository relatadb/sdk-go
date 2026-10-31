package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAssertionRelationRoutesAndBodies(t *testing.T) {
	var seen [][]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&b)
		}
		seen = append(seen, []any{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, b})
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	a := NewAssertionsClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	ctx := context.Background()
	calls := []func() error{
		func() error {
			_, e := a.Relate(ctx, "asr_a", "supports", "asr_b", "same passage", map[string]any{"pointer": "page=3"})
			return e
		},
		func() error { _, e := a.Relate(ctx, "asr_a", "contradicts", "asr_b", "dates differ", nil); return e },
		func() error { _, e := a.Unrelate(ctx, "rel/1", "misread"); return e },
		func() error { _, e := a.Assess(ctx, "asr_a", "disputed", "two witnesses disagree"); return e },
		func() error { _, e := a.EvidenceGraph(ctx, "asr_a", EvidenceGraphOptions{}); return e },
		func() error {
			_, e := a.EvidenceGraph(ctx, "asr/a", EvidenceGraphOptions{Depth: 3, Limit: 10, Policy: "trust", PolicyVersion: 2})
			return e
		},
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	nil_ := map[string]any(nil)
	want := [][]any{
		{"POST", "/v1/assertions/relations", "", map[string]any{"from": "asr_a", "to": "asr_b", "kind": "supports", "rationale": "same passage", "locator": map[string]any{"pointer": "page=3"}}},
		{"POST", "/v1/assertions/relations", "", map[string]any{"from": "asr_a", "to": "asr_b", "kind": "contradicts", "rationale": "dates differ"}},
		{"POST", "/v1/assertions/relations/rel%2F1/revoke", "", map[string]any{"reason": "misread"}},
		{"POST", "/v1/assertions/asr_a/assessment", "", map[string]any{"judgment": "disputed", "reason": "two witnesses disagree"}},
		{"GET", "/v1/assertions/asr_a/evidence-graph", "", nil_},
		{"GET", "/v1/assertions/asr%2Fa/evidence-graph", "depth=3&limit=10&policy=trust&policy_version=2", nil_},
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
}

func TestRelateSimilarAndMinSimilarityReachTheWire(t *testing.T) {
	var seen [][]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&b)
		}
		seen = append(seen, []any{r.URL.RawQuery, b})
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	a := NewAssertionsClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	ctx := context.Background()
	if _, err := a.RelateSimilar(ctx, "a", "b", "same photo", 0.93, "phash-v1", nil); err != nil {
		t.Fatal(err)
	}
	min := 0.5
	if _, err := a.EvidenceGraph(ctx, "a", EvidenceGraphOptions{MinSimilarity: &min}); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(seen)
	want := `[["",{"from":"a","kind":"similar_to","rationale":"same photo","similarity":{"method":"phash-v1","score":0.93},"to":"b"}],["min_similarity=0.5",null]]`
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
