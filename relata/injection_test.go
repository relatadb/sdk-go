package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #3211 — SQL-injection regression tests. Every payload feeds the characters
// `'`, `;`, `--`, `)` into object_type / embedding_slot / values and asserts a
// client-side rejection (identifiers, metric) or a safely contained value
// (escaped literal / bound parameter) before any HTTP call reaches the server.

// injectionServer records whether any request landed and, when one does, the
// SQL + params it carried.
func injectionServer(t *testing.T) (*httptest.Server, *bool, *string, *[]any) {
	t.Helper()
	hit := false
	sql := ""
	var params []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		sql, _ = body["sql"].(string)
		if p, ok := body["params"].([]any); ok {
			params = p
		}
		fmt.Fprint(w, `{"rows":[],"query_id":"q1","elapsed_ms":1}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hit, &sql, &params
}

var injectionPayloads = []string{
	"'",
	";",
	"--",
	")",
	"Person'; DROP TABLE Person; --",
	"x') OR ('1'='1",
}

func TestInjection_KNNSearch_RejectsBadObjectType(t *testing.T) {
	srv, hit, _, _ := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	for _, p := range injectionPayloads {
		if _, err := v.KNNSearch(context.Background(), p, "_emb", []float64{0.1}, 5, nil); err == nil {
			t.Errorf("object_type %q: expected rejection, got nil", p)
		}
	}
	if *hit {
		t.Fatal("an HTTP request was made despite client-side rejection")
	}
}

func TestInjection_KNNSearch_RejectsBadEmbeddingSlot(t *testing.T) {
	srv, hit, _, _ := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	for _, p := range injectionPayloads {
		if _, err := v.KNNSearch(context.Background(), "Person", p, []float64{0.1}, 5, nil); err == nil {
			t.Errorf("embedding_slot %q: expected rejection, got nil", p)
		}
	}
	if *hit {
		t.Fatal("an HTTP request was made despite client-side rejection")
	}
}

func TestInjection_HybridSearch_RejectsBadObjectType(t *testing.T) {
	srv, hit, _, _ := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	for _, p := range injectionPayloads {
		if _, err := v.HybridSearch(context.Background(), p, "q", nil); err == nil {
			t.Errorf("object_type %q: expected rejection, got nil", p)
		}
	}
	if *hit {
		t.Fatal("an HTTP request was made despite client-side rejection")
	}
}

func TestInjection_HybridSearch_RejectsBadMetric(t *testing.T) {
	srv, hit, _, _ := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	for _, m := range []string{"cosine; DROP TABLE x", "l2--", "dotproduct)", "'"} {
		if _, err := v.HybridSearch(context.Background(), "Person", "q", &HybridSearchOptions{Metric: m}); err == nil {
			t.Errorf("metric %q: expected rejection, got nil", m)
		}
	}
	if *hit {
		t.Fatal("an HTTP request was made despite client-side rejection")
	}
	// The allowlisted metrics still pass validation.
	for _, m := range []string{"cosine", "l2", "dotproduct"} {
		if err := validateMetric(m); err != nil {
			t.Errorf("metric %q must be allowed, got %v", m, err)
		}
	}
}

func TestInjection_SimilarTo_RejectsBadObjectType(t *testing.T) {
	srv, hit, _, _ := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	for _, p := range injectionPayloads {
		if _, err := v.SimilarTo(context.Background(), p, "ref-1", nil); err == nil {
			t.Errorf("object_type %q: expected rejection, got nil", p)
		}
	}
	if *hit {
		t.Fatal("an HTTP request was made despite client-side rejection")
	}
}

func TestInjection_SimilarTo_BindsReferenceID(t *testing.T) {
	srv, hit, sql, params := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	payload := "x'); DROP TABLE Person; --"
	if _, err := v.SimilarTo(context.Background(), "Person", payload, nil); err != nil {
		t.Fatal(err)
	}
	if !*hit {
		t.Fatal("expected the query to reach the server")
	}
	// The value is bound as $1 — it must not appear in the SQL text at all.
	if strings.Contains(*sql, payload) || strings.Contains(*sql, "DROP TABLE") {
		t.Fatalf("payload leaked into SQL: %q", *sql)
	}
	if len(*params) != 1 || (*params)[0] != payload {
		t.Fatalf("params = %v, want the bound payload", *params)
	}
}

func TestInjection_PathsBetween_EscapesValues(t *testing.T) {
	srv, hit, sql, _ := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	payload := "a'); DROP TABLE Person; --"
	if _, err := PathsBetween(payload, "b", 3).Purpose("analytics").Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !*hit {
		t.Fatal("expected the query to reach the server")
	}
	assertContainedLiteral(t, *sql, payload)
}

func TestInjection_LookupIdentity_EscapesValue(t *testing.T) {
	srv, hit, sql, _ := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	payload := "+1'); DROP TABLE Person; --"
	if _, err := LookupIdentity(payload).Purpose("analytics").Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !*hit {
		t.Fatal("expected the query to reach the server")
	}
	assertContainedLiteral(t, *sql, payload)
}

func TestInjection_EraseSubject_EscapesValues(t *testing.T) {
	srv, hit, sql, _ := injectionServer(t)
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "gdpr"})
	payload := "subj'); DROP TABLE Person; --"
	if _, err := c.EraseSubject(context.Background(), "gdpr", payload, "reason"); err != nil {
		t.Fatal(err)
	}
	if !*hit {
		t.Fatal("expected the query to reach the server")
	}
	assertContainedLiteral(t, *sql, payload)
}

func TestInjection_HybridSearchBuilder_RejectsBadObjectType(t *testing.T) {
	for _, p := range injectionPayloads {
		if _, err := HybridSearch(p, "q", 5); err == nil {
			t.Errorf("object_type %q: expected rejection, got nil", p)
		}
	}
}

// assertContainedLiteral verifies that payload appears in sql only as a single
// backslash-escaped string literal: every `'` in the payload is emitted as
// `\'`, so the payload cannot terminate the literal early and break out.
func assertContainedLiteral(t *testing.T, sql, payload string) {
	t.Helper()
	escaped := escapeSQLString(payload)
	if !strings.Contains(sql, "'"+escaped+"'") {
		t.Fatalf("sql %q does not contain the escaped literal %q", sql, "'"+escaped+"'")
	}
	// The raw payload (with its unescaped quote) must not be present.
	if strings.Contains(sql, payload) {
		t.Fatalf("raw payload leaked into sql: %q", sql)
	}
}
