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
