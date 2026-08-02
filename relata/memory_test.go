package relata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewMemory_RequiresPurpose(t *testing.T) {
	_, err := NewMemory("http://localhost:9090", "", nil)
	if !errors.Is(err, ErrPurposeRequired) {
		t.Fatalf("err = %v, want ErrPurposeRequired", err)
	}
	// whitespace-only purpose is also rejected
	_, err = NewMemory("http://localhost:9090", "   ", nil)
	if !errors.Is(err, ErrPurposeRequired) {
		t.Fatalf("err = %v, want ErrPurposeRequired for whitespace", err)
	}
}

func TestMemory_Add(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		// MCP envelope wrapping the inner result.
		inner := `{"id":"mem-1"}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, err := NewMemory(srv.URL, "agent-notes", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Add(context.Background(), "Alice prefers dark mode")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id != "mem-1" {
		t.Fatalf("id = %q, want mem-1", id)
	}
	if gotMethod != "POST" || gotPath != "/memory/remember" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody["purpose"] != "agent-notes" || gotBody["content"] != "Alice prefers dark mode" {
		t.Fatalf("payload = %v", gotBody)
	}
	if gotBody["memory_class"] != "semantic" {
		t.Fatalf("memory_class = %v", gotBody["memory_class"])
	}
}

func TestMemory_Search(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		inner := `{"rows":[{"id":"mem-1","content":"x","score":0.9}]}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "agent-notes", &MemoryOptions{SessionID: "sess-1"})
	rows, err := m.Search(context.Background(), "ui prefs", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 1 || rows[0]["id"] != "mem-1" {
		t.Fatalf("rows = %v", rows)
	}
	if gotQuery == "" {
		t.Fatal("no query string")
	}
	// purpose + session_id + top_k must be present
	if !contains(gotQuery, "purpose=agent-notes") || !contains(gotQuery, "session_id=sess-1") || !contains(gotQuery, "top_k=5") {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestMemory_Search_SendsADR145RetrievalQualityParams(t *testing.T) {
	// #2674: CONFIDENCE/RECENCY/BUDGET/FORGETTING_CURVE/CANCEL_WHEN must be
	// reachable as MemoryOptions on Search, not just top_k/session_id/as_of.
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		inner := `{"rows":[]}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "agent-notes", nil)
	_, err := m.Search(context.Background(), "ui prefs",
		WithMinConfidence(0.6),
		WithRecencyHalfLife(3600),
		WithBudgetTokens(500),
		WithStabilityDays(7),
		WithCancelThreshold(0.95),
	)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, want := range []string{
		"min_confidence=0.6", "recency_half_life_secs=3600", "budget_tokens=500",
		"stability_days=7", "cancel_threshold=0.95",
	} {
		if !contains(gotQuery, want) {
			t.Fatalf("query = %q, want %q", gotQuery, want)
		}
	}
}

func TestMemory_Search_OmitsADR145ParamsWhenUnset(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		inner := `{"rows":[]}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "agent-notes", nil)
	if _, err := m.Search(context.Background(), "ui prefs"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, unwanted := range []string{
		"min_confidence", "recency_half_life_secs", "budget_tokens",
		"stability_days", "cancel_threshold",
	} {
		if contains(gotQuery, unwanted) {
			t.Fatalf("query = %q, unexpected %q", gotQuery, unwanted)
		}
	}
}

func TestMemory_SearchDetailed_SurfacesRecallCostTokensAndCancelled(t *testing.T) {
	// #2674: the read-only BUDGET/CANCEL_WHEN response fields must be
	// observable, not just settable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner := `{"rows":[{"id":"mem-1"}],"recall_cost_tokens":42,"cancelled":true}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "agent-notes", nil)
	result, err := m.SearchDetailed(context.Background(), "ui prefs",
		WithBudgetTokens(50), WithCancelThreshold(0.9))
	if err != nil {
		t.Fatalf("SearchDetailed: %v", err)
	}
	if result.RecallCostTokens != 42 {
		t.Fatalf("RecallCostTokens = %d", result.RecallCostTokens)
	}
	if !result.Cancelled {
		t.Fatal("Cancelled = false, want true")
	}
	if len(result.Rows) != 1 || result.Rows[0]["id"] != "mem-1" {
		t.Fatalf("Rows = %v", result.Rows)
	}
}

func TestMemory_Forget(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		inner := `{"memory_item_id":"mem-1","policy":"retention","forget_at_ns":123}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "agent-notes", nil)
	out, err := m.Forget(context.Background(), "mem-1")
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if gotMethod != "DELETE" || gotPath != "/memory/forget/mem-1" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if out["policy"] != "retention" {
		t.Fatalf("policy decision = %v", out)
	}
}

func TestMemory_Get_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner := `{}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "agent-notes", nil)
	got, err := m.Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for not-found, got %v", got)
	}
}

func TestMemory_AddBatch(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		inner := `{"results":[{"id":"a"},{"id":"b"}]}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "agent-notes", &MemoryOptions{SessionID: "s1"})
	ids, err := m.AddBatch(context.Background(), []any{"first", map[string]any{"content": "second", "memory_class": "episodic"}})
	if err != nil {
		t.Fatalf("AddBatch: %v", err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v", ids)
	}
	items, ok := gotBody["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %v", gotBody["items"])
	}
	first := items[0].(map[string]any)
	if first["session_id"] != "s1" {
		t.Fatalf("default session not applied: %v", first)
	}
}

func TestMemory_Associate(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		inner := `{"source_id":"a","target_id":"b","relation":"derives"}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "p", nil)
	out, err := m.Associate(context.Background(), "a", "b", "derives", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/memory/associate" || out["relation"] != "derives" {
		t.Fatalf("path=%q out=%v", gotPath, out)
	}
}

func TestMemory_Resolve(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		inner := `{"resolved_id":"mem-10"}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"isError":false}`, strconvQuote(inner))
	}))
	defer srv.Close()

	m, _ := NewMemory(srv.URL, "agent-notes", nil)
	out, err := m.Resolve(context.Background(), "mem-9")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// The server route is GET-only (#2675) — a POST body/policy param 405s.
	if gotMethod != "GET" || gotPath != "/memory/resolve/mem-9" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if !contains(gotQuery, "purpose=agent-notes") {
		t.Fatalf("query = %q, want purpose=agent-notes", gotQuery)
	}
	if out["resolved_id"] != "mem-10" {
		t.Fatalf("resolved_id = %v", out)
	}
}

// strconvQuote is a tiny helper kept to avoid pulling "strconv" into the test
// file header when only one call site needs it.
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
