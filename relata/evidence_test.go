package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestEvidenceRoutesAndBodies(t *testing.T) {
	var seen [][]any
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
		w.Write([]byte(`{"items":[],"citations":[{"outcome":"verified"}]}`))
	}))
	defer srv.Close()
	e := NewEvidenceClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	ctx := context.Background()
	if _, err := e.List(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Get(ctx, "evd_a/b"); err != nil {
		t.Fatal(err)
	}
	out, err := e.Replay(ctx, "evd_1", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Replay(ctx, "evd_1", false); err != nil {
		t.Fatal(err)
	}
	if cs, _ := out["citations"].([]any); len(cs) != 1 {
		t.Fatalf("citations: %v", out)
	}
	want := [][]any{
		{"GET", "/evidence", "limit=5", map[string]any(nil)},
		{"GET", "/evidence/evd_a%2Fb", "", map[string]any(nil)},
		{"POST", "/evidence/evd_1/replay", "", map[string]any{"include_content": true}},
		{"POST", "/evidence/evd_1/replay", "", map[string]any{}},
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("got %#v want %#v", seen, want)
	}
	h := EvidenceHeaders(7)
	if h["X-Relata-Record-Evidence"] != "true" || h["X-Relata-Evidence-Retention-Days"] != "7" {
		t.Fatalf("headers %v", h)
	}
}
