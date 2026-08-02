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

// ─── Namespace (#2491, mirrors sdks/python/relata/namespace.py) ──────────────

func TestNamespace_BoundHandle(t *testing.T) {
	c := New("http://localhost:9090", nil)
	ns := c.Namespace("Document")
	if ns == nil || ns.Name != "Document" {
		t.Fatalf("ns = %+v", ns)
	}
}

func TestNamespace_Query_DelegatesToSearch(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"rows":[{"id":"d1","title":"hit"}],"query_id":"q1","elapsed_ms":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	res, err := c.Namespace("Document").Query(context.Background(), SearchRequest{
		Text:        "graph retrieval",
		MatchColumn: "title",
		Filters:     []SearchFilter{{Field: "status", Op: "eq", Value: "published"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["from"] != "Document" {
		t.Fatalf("from = %v", gotBody["from"])
	}
	rankBy, _ := gotBody["rank_by"].([]any)
	if len(rankBy) != 3 || rankBy[0] != "bm25" || rankBy[2] != "graph retrieval" {
		t.Fatalf("rank_by = %v", gotBody["rank_by"])
	}
	if len(res.Rows) != 1 || res.Rows[0]["id"] != "d1" {
		t.Fatalf("rows = %v", res.Rows)
	}
}

func TestNamespace_Query_RejectsUnsafeName(t *testing.T) {
	c := New("http://localhost:9090", nil)
	_, err := c.Namespace("Doc; DROP TABLE").Query(context.Background(), SearchRequest{})
	if err == nil || !strings.Contains(err.Error(), "invalid namespace") {
		t.Fatalf("err = %v", err)
	}
}

func TestNamespace_Write_PostsToIngestAuto(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"object_type":"Document","type_created":true,"inserted":2}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "rag"})
	out, err := c.Namespace("Document").Write(context.Background(),
		[]map[string]any{{"id": "d1", "title": "A"}, {"id": "d2", "title": "B"}},
		nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/ingest/auto" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotBody["object_type"] != "Document" {
		t.Fatalf("object_type = %v", gotBody["object_type"])
	}
	if gotBody["purpose"] != "rag" {
		t.Fatalf("purpose = %v", gotBody["purpose"])
	}
	if out["type_created"] != true {
		t.Fatalf("out = %v", out)
	}
}

func TestNamespace_Write_SchemaOverrides(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	_, err := c.Namespace("Doc").Write(context.Background(),
		[]map[string]any{{"id": "1"}},
		&NamespaceWriteOptions{Schema: map[string]string{"embedding": "bge-m3:text:1.0:1024"}})
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := gotBody["schema"].(map[string]any)
	if schema["embedding"] != "bge-m3:text:1.0:1024" {
		t.Fatalf("schema = %v", gotBody["schema"])
	}
}

func TestNamespace_Get_PointLookup(t *testing.T) {
	var gotSQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotSQL, _ = body["sql"].(string)
		fmt.Fprint(w, `{"rows":[{"id":"d42","title":"Found"}],"columns":["id","title"]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	row, err := c.Namespace("Document").Get(context.Background(), "d42", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotSQL, "SELECT * FROM Document WHERE id = 'd42'") {
		t.Fatalf("sql = %q", gotSQL)
	}
	if row == nil || row["id"] != "d42" {
		t.Fatalf("row = %v", row)
	}
}

func TestNamespace_Get_MissingReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"rows":[],"columns":[]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	row, err := c.Namespace("Document").Get(context.Background(), "nope", nil)
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("expected nil, got %v", row)
	}
}

func TestNamespace_DeleteAll_Tombstones(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/query" {
			fmt.Fprint(w, `{"rows":[{"id":"d1"},{"id":"d2"}],"columns":["id"]}`)
			return
		}
		// /ingest/auto — assert tombstones
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		rows, _ := body["rows"].([]any)
		for _, r := range rows {
			row, _ := r.(map[string]any)
			if row["_deleted"] != true {
				t.Errorf("expected _deleted=true, got row=%v", row)
			}
		}
		fmt.Fprint(w, `{"inserted":2}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "rag"})
	out, err := c.Namespace("Document").DeleteAll(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["deleted"].(int) != 2 {
		t.Fatalf("out = %v", out)
	}
	wantPaths := []string{"/query", "/ingest/auto"}
	for i, p := range wantPaths {
		if paths[i] != p {
			t.Fatalf("path[%d] = %s, want %s", i, paths[i], p)
		}
	}
}

func TestNamespace_DeleteAll_EmptyIsNoop(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, `{"rows":[],"columns":["id"]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "rag"})
	out, err := c.Namespace("Document").DeleteAll(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["deleted"].(int) != 0 {
		t.Fatalf("out = %v", out)
	}
	if len(paths) != 1 || paths[0] != "/query" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestNamespace_BranchFrom(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"branched":true,"name":"Draft"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	out, err := c.Namespace("Draft").BranchFrom(context.Background(), "Document", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/namespaces/Draft/branch" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotBody["from"] != "Document" {
		t.Fatalf("body = %v", gotBody)
	}
	if out["branched"] != true {
		t.Fatalf("out = %v", out)
	}
}

// ─── Schema-BRANCH CRUD + edge types (#2497) ─────────────────────────────────

func TestListEdgeTypes(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"edges":[{"from_type":"Person","to_type":"Org","label":"DIRECTOR_OF"}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	out, err := c.ListEdgeTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || gotPath != "/types/edges" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	edges, _ := out["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("edges = %v", out)
	}
}

func TestRegisterEdgeType(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"created":true}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	out, err := c.RegisterEdgeType(context.Background(), "Person", "Org", "DIRECTOR_OF")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/types/edges" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody["from_type"] != "Person" || gotBody["to_type"] != "Org" || gotBody["label"] != "DIRECTOR_OF" {
		t.Fatalf("body = %v", gotBody)
	}
	if out["created"] != true {
		t.Fatalf("out = %v", out)
	}
}

func TestCreateSchemaBranch(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"name":"exp-branch","created":true}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	out, err := c.CreateSchemaBranch(context.Background(), "exp-branch", "main")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/schema/branches/exp-branch" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody["from"] != "main" {
		t.Fatalf("body = %v", gotBody)
	}
	if out["name"] != "exp-branch" {
		t.Fatalf("out = %v", out)
	}
}

func TestDeleteSchemaBranch(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"name":"exp-branch","deleted":true}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	out, err := c.DeleteSchemaBranch(context.Background(), "exp-branch")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/schema/branches/exp-branch" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if out["deleted"] != true {
		t.Fatalf("out = %v", out)
	}
}
