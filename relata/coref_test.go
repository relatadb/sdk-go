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

// Tests for coref.go — session-scoped coreference resolution (RAG epic,
// #4530). Mirrors sdks/python/tests/test_coref.py (#4580 TS/Go parity).
//
// fakeMemoryServer is a minimal in-process fake reproducing the /memory/*
// contract this module depends on: remember inserts, consolidate supersedes
// (pop old id, insert new — mirroring the server's bi-temporal
// retract+re-insert), and recall filters by exact session_id match.

type fakeMemoryRow struct {
	Content   string
	SessionID string
}

type fakeMemoryServer struct {
	rows             map[string]fakeMemoryRow
	rememberCalls    int
	consolidateCalls int
	recallCalls      int
	nextID           int
	srv              *httptest.Server
}

func newFakeMemoryServer() *fakeMemoryServer {
	f := &fakeMemoryServer{rows: map[string]fakeMemoryRow{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeMemoryServer) newID() string {
	f.nextID++
	return fmt.Sprintf("mem-%d", f.nextID)
}

func mcpEnvelopeBytes(inner map[string]any) []byte {
	innerJSON, _ := json.Marshal(inner)
	env := map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(innerJSON)}},
		"isError": false,
	}
	b, _ := json.Marshal(env)
	return b
}

func (f *fakeMemoryServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/memory/remember":
		f.rememberCalls++
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		id := f.newID()
		sessionID, _ := body["session_id"].(string)
		content, _ := body["content"].(string)
		f.rows[id] = fakeMemoryRow{Content: content, SessionID: sessionID}
		w.Write(mcpEnvelopeBytes(map[string]any{"id": id, "session_id": sessionID}))
	case "/memory/consolidate":
		f.consolidateCalls++
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		oldID, _ := body["id"].(string)
		old, ok := f.rows[oldID]
		delete(f.rows, oldID)
		newID := f.newID()
		sessionID := ""
		if ok {
			sessionID = old.SessionID
		}
		content, _ := body["content"].(string)
		f.rows[newID] = fakeMemoryRow{Content: content, SessionID: sessionID}
		w.Write(mcpEnvelopeBytes(map[string]any{"new_id": newID}))
	case "/memory/recall":
		f.recallCalls++
		sid := r.URL.Query().Get("session_id")
		rows := []map[string]any{}
		for id, row := range f.rows {
			if row.SessionID == sid {
				rows = append(rows, map[string]any{"id": id, "content": row.Content, "session_id": row.SessionID})
			}
		}
		w.Write(mcpEnvelopeBytes(map[string]any{"rows": rows}))
	default:
		http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
	}
}

func (f *fakeMemoryServer) close() { f.srv.Close() }

func newCorefResolverForTest(t *testing.T) (*CorefResolver, *fakeMemoryServer) {
	t.Helper()
	f := newFakeMemoryServer()
	t.Cleanup(f.close)
	m, err := NewMemory(f.srv.URL, "agent-notes", nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewCorefResolver(m), f
}

// ── the acceptance-criteria regression test ─────────────────────────────────

func TestCorefResolver_TwoTurnSequenceResolvesPronounToPriorSubject(t *testing.T) {
	coref, _ := newCorefResolverForTest(t)
	ctx := context.Background()

	if err := coref.RememberSubject(ctx, "session-1", "India"); err != nil {
		t.Fatalf("RememberSubject: %v", err)
	}
	resolved, err := coref.Resolve(ctx, "Where is it located?", "session-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != "Where is India located?" {
		t.Fatalf("resolved = %q, want %q", resolved, "Where is India located?")
	}
}

// ── acceptance: exactly one row, never a growing list ───────────────────────

func TestCorefResolver_StateIsExactlyOneRowAndNeverGrows(t *testing.T) {
	coref, f := newCorefResolverForTest(t)
	ctx := context.Background()

	if err := coref.RememberSubject(ctx, "session-1", "India"); err != nil {
		t.Fatal(err)
	}
	if f.rememberCalls != 1 || len(f.rows) != 1 {
		t.Fatalf("after turn 1: rememberCalls=%d rows=%d", f.rememberCalls, len(f.rows))
	}

	if _, err := coref.Resolve(ctx, "Where is it located?", "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := coref.RememberSubject(ctx, "session-1", "Paris"); err != nil { // turn 2 supersedes turn 1
		t.Fatal(err)
	}
	if f.rememberCalls != 1 {
		t.Fatalf("rememberCalls = %d, want 1 (no second add)", f.rememberCalls)
	}
	if f.consolidateCalls != 1 {
		t.Fatalf("consolidateCalls = %d, want 1", f.consolidateCalls)
	}
	if len(f.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(f.rows))
	}

	if err := coref.RememberSubject(ctx, "session-1", "Berlin"); err != nil { // turn 3 supersedes turn 2
		t.Fatal(err)
	}
	if f.rememberCalls != 1 || f.consolidateCalls != 2 || len(f.rows) != 1 {
		t.Fatalf("after turn 3: rememberCalls=%d consolidateCalls=%d rows=%d",
			f.rememberCalls, f.consolidateCalls, len(f.rows))
	}

	resolved, err := coref.Resolve(ctx, "Where is it now?", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "Where is Berlin now?" {
		t.Fatalf("resolved = %q, want %q", resolved, "Where is Berlin now?")
	}
}

// ── acceptance: single-turn callers pay no extra cost ───────────────────────

func TestCorefResolver_NoSessionIDMakesZeroNetworkCalls(t *testing.T) {
	coref, f := newCorefResolverForTest(t)
	ctx := context.Background()

	resolved, err := coref.Resolve(ctx, "Where is it located?", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "Where is it located?" {
		t.Fatalf("resolved = %q", resolved)
	}
	if f.recallCalls != 0 || f.rememberCalls != 0 {
		t.Fatalf("recallCalls=%d rememberCalls=%d, want 0/0", f.recallCalls, f.rememberCalls)
	}
}

func TestCorefResolver_QueryWithoutUnresolvedPronounMakesZeroNetworkCalls(t *testing.T) {
	coref, f := newCorefResolverForTest(t)
	ctx := context.Background()

	resolved, err := coref.Resolve(ctx, "What is the capital of France?", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "What is the capital of France?" {
		t.Fatalf("resolved = %q", resolved)
	}
	if f.recallCalls != 0 {
		t.Fatalf("recallCalls = %d, want 0", f.recallCalls)
	}
}

func TestCorefResolver_PronounWithNoPriorSubjectReturnsQueryUnchanged(t *testing.T) {
	coref, f := newCorefResolverForTest(t)
	ctx := context.Background()

	resolved, err := coref.Resolve(ctx, "Where is it located?", "session-never-seen")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "Where is it located?" {
		t.Fatalf("resolved = %q", resolved)
	}
	if f.recallCalls != 1 {
		t.Fatalf("recallCalls = %d, want 1", f.recallCalls)
	}
}

func TestCorefResolver_LocalAntecedentSuppressesTheTrip(t *testing.T) {
	coref, f := newCorefResolverForTest(t)
	ctx := context.Background()

	if err := coref.RememberSubject(ctx, "session-1", "India"); err != nil {
		t.Fatal(err)
	}
	recallCallsAfterRemember := f.recallCalls

	query := "What did Marie Curie discover and how did it change physics?"
	resolved, err := coref.Resolve(ctx, query, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != query {
		t.Fatalf("resolved = %q, want unchanged %q", resolved, query)
	}
	if f.recallCalls != recallCallsAfterRemember {
		t.Fatalf("recallCalls changed: %d -> %d", recallCallsAfterRemember, f.recallCalls)
	}
}

// ── acceptance: cross-session isolation ─────────────────────────────────────

func TestCorefResolver_CrossSessionIsolation(t *testing.T) {
	coref, _ := newCorefResolverForTest(t)
	ctx := context.Background()

	if err := coref.RememberSubject(ctx, "session-a", "India"); err != nil {
		t.Fatal(err)
	}
	resolved, err := coref.Resolve(ctx, "Where is it located?", "session-b")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "Where is it located?" {
		t.Fatalf("resolved = %q, want unchanged", resolved)
	}
}

// ── SubjectFromHit / HasUnresolvedPronoun unit coverage ─────────────────────

func TestSubjectFromHit_UsesFirstEntityID(t *testing.T) {
	hit := RagHit{EntityIDs: []string{"India", "New Delhi"}}
	subject, ok := SubjectFromHit(hit)
	if !ok || subject != "India" {
		t.Fatalf("subject=%q ok=%v, want India/true", subject, ok)
	}
}

func TestSubjectFromHit_ReturnsFalseWithoutEntityIDs(t *testing.T) {
	hit := RagHit{EntityIDs: []string{}}
	_, ok := SubjectFromHit(hit)
	if ok {
		t.Fatal("ok = true, want false")
	}
}

func TestHasUnresolvedPronoun(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"Where is it located?", true},
		{"What is the capital of France?", false},
		{"What did Marie Curie discover and how did it change physics?", false},
	}
	for _, c := range cases {
		if got := HasUnresolvedPronoun(c.query); got != c.want {
			t.Errorf("HasUnresolvedPronoun(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}
