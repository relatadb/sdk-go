package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestInvestigationRoutesAndBodies(t *testing.T) {
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
	c := NewInvestigationClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	ctx := context.Background()
	calls := []func() error{
		func() error { _, err := c.AlertEvidence(ctx, "a/1", EvidenceBounds{}); return err },
		func() error {
			_, err := c.AlertEvidence(ctx, "a1", EvidenceBounds{MaxEvents: 5, DeadlineMs: 900})
			return err
		},
		func() error { _, err := c.AlertTimeline(ctx, "a1", EvidenceBounds{}); return err },
		func() error { _, err := c.AlertTimeline(ctx, "a1", EvidenceBounds{DeadlineMs: 10}); return err },
		func() error { _, err := c.Pivot(ctx, "a1", PivotRequest{}); return err },
		func() error {
			_, err := c.Pivot(ctx, "a1", PivotRequest{Field: "user", Value: "alice", TargetField: "owner", TargetTypes: []string{"Session"}, Limit: 3, DeadlineMs: 50})
			return err
		},
		func() error {
			_, err := c.CreateSavedSearch(ctx, SavedSearch{Name: "n", SQL: "SELECT 1", Purpose: "audit", Description: "d", Params: []any{"x"}})
			return err
		},
		func() error { _, err := c.ListSavedSearches(ctx, "", 0); return err },
		func() error { _, err := c.ListSavedSearches(ctx, "fail", 7); return err },
		func() error { _, err := c.GetSavedSearch(ctx, "ss/1"); return err },
		func() error {
			_, err := c.UpdateSavedSearch(ctx, "ss-1", SavedSearch{Name: "n", SQL: "SELECT 2"}, 3)
			return err
		},
		func() error { _, err := c.DeleteSavedSearch(ctx, "ss-1"); return err },
		func() error { _, err := c.RunSavedSearch(ctx, "ss-1", nil); return err },
		func() error { _, err := c.RunSavedSearch(ctx, "ss-1", []any{float64(1), nil}); return err },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	nb := map[string]any(nil)
	want := [][]any{
		{"GET", "/v1/alerts/a%2F1/evidence", "", nb},
		{"GET", "/v1/alerts/a1/evidence", "deadline_ms=900&max_events=5", nb},
		{"GET", "/v1/alerts/a1/timeline", "", nb},
		{"GET", "/v1/alerts/a1/timeline", "deadline_ms=10", nb},
		{"POST", "/v1/alerts/a1/pivot", "", map[string]any{}},
		{"POST", "/v1/alerts/a1/pivot", "", map[string]any{"field": "user", "value": "alice", "target_field": "owner", "target_types": []any{"Session"}, "limit": float64(3), "deadline_ms": float64(50)}},
		{"POST", "/v1/search/saved", "", map[string]any{"name": "n", "sql": "SELECT 1", "purpose": "audit", "description": "d", "params": []any{"x"}}},
		{"GET", "/v1/search/saved", "", nb},
		{"GET", "/v1/search/saved", "limit=7&q=fail", nb},
		{"GET", "/v1/search/saved/ss%2F1", "", nb},
		{"PUT", "/v1/search/saved/ss-1", "", map[string]any{"name": "n", "sql": "SELECT 2", "expected_version": float64(3)}},
		{"DELETE", "/v1/search/saved/ss-1", "", nb},
		{"POST", "/v1/search/saved/ss-1/run", "", map[string]any{}},
		{"POST", "/v1/search/saved/ss-1/run", "", map[string]any{"params": []any{float64(1), nil}}},
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
	status = http.StatusForbidden
	if _, err := c.Pivot(ctx, "a1", PivotRequest{Value: "bob"}); err == nil {
		t.Fatal("a 403 must surface as an error")
	}
}
