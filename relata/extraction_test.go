package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestExtractionRoutesAndBodies(t *testing.T) {
	var seen [][]any
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped" {
			t.Errorf("auth not inherited: %v", r.Header)
		}
		var b map[string]any
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&b)
		}
		seen = append(seen, []any{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, b})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	e := NewExtractionClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	ctx := context.Background()
	s := map[string]any{"kind": "entity", "confidence": 0.9}
	fix := map[string]any{"type": "Person"}
	yes, no, min := true, false, 0.25
	calls := []func() error{
		func() error { _, err := e.Submit(ctx, []map[string]any{s}); return err },
		func() error { _, err := e.List(ctx, SuggestionFilter{}); return err },
		func() error {
			_, err := e.List(ctx, SuggestionFilter{State: "pending", Kind: "entity", Definition: "kb", LowConfidence: &yes, Stale: &no, MinConfidence: &min, Limit: 5})
			return err
		},
		func() error { _, err := e.Get(ctx, "sug/1"); return err },
		func() error { _, err := e.Accept(ctx, "sug_1", "ok", false); return err },
		func() error { _, err := e.Accept(ctx, "sug_1", "", true); return err },
		func() error { _, err := e.Reject(ctx, "sug_1", "wrong"); return err },
		func() error { _, err := e.Correct(ctx, "sug_1", fix, "typo"); return err },
		func() error { _, err := e.Reopen(ctx, "sug_1", ""); return err },
		func() error { _, err := e.Apply(ctx, "kb", nil); return err },
		func() error { _, err := e.Apply(ctx, "kb", []string{"sug_1"}); return err },
		func() error {
			_, err := e.EntitiesExist(ctx, []map[string]any{{"type": "Person", "match": map[string]any{"email": "a@b.c"}}}, "")
			return err
		},
		func() error { _, err := e.EntitiesExist(ctx, []map[string]any{}, "kb"); return err },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	nb := map[string]any(nil)
	want := [][]any{
		{"POST", "/v1/ontology/suggestions", "", map[string]any{"suggestions": []any{s}}},
		{"GET", "/v1/ontology/suggestions", "", nb},
		{"GET", "/v1/ontology/suggestions", "definition=kb&kind=entity&limit=5&low_confidence=true&min_confidence=0.25&stale=false&state=pending", nb},
		{"GET", "/v1/ontology/suggestions/sug%2F1", "", nb},
		{"POST", "/v1/ontology/suggestions/sug_1/decision", "", map[string]any{"decision": "accept", "reason": "ok"}},
		{"POST", "/v1/ontology/suggestions/sug_1/decision", "", map[string]any{"decision": "accept", "reason": "", "accept_low_confidence": true}},
		{"POST", "/v1/ontology/suggestions/sug_1/decision", "", map[string]any{"decision": "reject", "reason": "wrong"}},
		{"POST", "/v1/ontology/suggestions/sug_1/decision", "", map[string]any{"decision": "correct", "reason": "typo", "correction": fix}},
		{"POST", "/v1/ontology/suggestions/sug_1/decision", "", map[string]any{"decision": "reopen", "reason": ""}},
		{"POST", "/v1/ontology/suggestions/apply", "", map[string]any{"definition": "kb"}},
		{"POST", "/v1/ontology/suggestions/apply", "", map[string]any{"definition": "kb", "suggestion_ids": []any{"sug_1"}}},
		{"POST", "/v1/entities/exists", "", map[string]any{"candidates": []any{map[string]any{"type": "Person", "match": map[string]any{"email": "a@b.c"}}}}},
		{"POST", "/v1/entities/exists", "", map[string]any{"candidates": []any{}, "definition": "kb"}},
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
	if _, err := e.Accept(ctx, "sug_1", "", false); err == nil {
		t.Fatal("a 409 must surface as an error")
	}
}
