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

// ---- GovernanceClient -------------------------------------------------------

func TestGovernanceClient_ListRules(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, `{"rules":[{"id":"r1","name":"x"},{"id":"r2","name":"y"}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "audit", Tenant: "org-1"})
	g := NewGovernanceClient(c)
	rules, err := g.ListRules(context.Background(), "Person")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/rules" || gotQuery != "object_type=Person" {
		t.Fatalf("path=%q query=%q", gotPath, gotQuery)
	}
	if len(rules) != 2 || rules[0]["id"] != "r1" {
		t.Fatalf("rules = %v", rules)
	}
}

func TestGovernanceClient_CreateRule(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"id":"r1","name":"x"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "audit"})
	g := NewGovernanceClient(c)
	out, err := g.CreateRule(context.Background(), map[string]any{"name": "x", "condition_sql": "age > 18", "target_type": "Person"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/v1/rules" || gotQuery != "purpose=audit" {
		t.Fatalf("method/path/query = %s %s %s", gotMethod, gotPath, gotQuery)
	}
	if gotBody["name"] != "x" || gotBody["condition_sql"] != "age > 18" || gotBody["target_type"] != "Person" {
		t.Fatalf("body = %v", gotBody)
	}
	if out["id"] != "r1" {
		t.Fatalf("out = %v", out)
	}
}

func TestGovernanceClient_CreateRule_PurposeRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c := newTestClient(srv, nil) // no default purpose
	g := NewGovernanceClient(c)
	_, err := g.CreateRule(context.Background(), map[string]any{"name": "x"}, nil)
	if !errors.Is(err, ErrPurposeRequired) {
		t.Fatalf("err = %v, want ErrPurposeRequired", err)
	}
}

func TestGovernanceClient_SuppressRule(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"rule_id":"r1"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	g := NewGovernanceClient(c)
	_, err := g.SuppressRule(context.Background(), "r1", "entity-42", &SuppressRuleOptions{Condition: "known FP"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["entity_id"] != "entity-42" || gotBody["condition"] != "known FP" {
		t.Fatalf("body = %v", gotBody)
	}
}

func TestGovernanceClient_DisableRule(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"id":"r1","disabled":true}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	g := NewGovernanceClient(c)
	if _, err := g.DisableRule(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/v1/rules/r1" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
}

func TestGovernanceClient_SubmitDSAR(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, `{"dsar_id":"d1"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	g := NewGovernanceClient(c)
	if _, err := g.SubmitDSAR(context.Background(), "alice@example.com", "gdpr-art-15", &SubmitDSAROptions{Scope: "email"}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || gotPath != "/v1/gdpr/dsar" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if !contains(gotQuery, "subject_identity=alice") || !contains(gotQuery, "scope=email") {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestGovernanceClient_UpdateAlert(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"id":"a1","status":"ack"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	g := NewGovernanceClient(c)
	if _, err := g.UpdateAlert(context.Background(), "a1", &UpdateAlertOptions{Status: "ack", Assignee: "bob"}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PATCH" || gotPath != "/v1/alerts/a1" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
}

// ---- AuditClient ------------------------------------------------------------

func TestAuditClient_Log(t *testing.T) {
	var paths, queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		queries = append(queries, r.URL.RawQuery)
		fmt.Fprint(w, `{"entries":[],"chain_valid":true}`)
	}))
	defer srv.Close()

	a := NewAuditClient(newTestClient(srv, nil))
	if _, err := a.Log(context.Background(), &AuditLogOptions{Limit: 5, Table: "Person", TenantID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if paths[0] != "/v1/audit/log" || !contains(queries[0], "limit=5") || !contains(queries[0], "table=Person") || !contains(queries[0], "tenant_id=t1") {
		t.Fatalf("log = %s?%s", paths[0], queries[0])
	}
}

// ---- BackupClient -----------------------------------------------------------

func TestBackupClient_CreateAndRestore(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		switch r.URL.Path {
		case "/admin/backup":
			fmt.Fprint(w, `{"snapshot_id":"snap-1","kind":"full","size_bytes":1024}`)
		case "/admin/restore":
			fmt.Fprint(w, `{"restore_id":"res-1"}`)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	b := NewBackupClient(c)
	ref, err := b.Create(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ref.SnapshotID != "snap-1" || ref.Kind != "full" || ref.SizeBytes != 1024 {
		t.Fatalf("ref = %+v", ref)
	}
	if gotPath != "/admin/backup" || gotMethod != "POST" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	rid, err := b.Restore(context.Background(), "snap-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rid != "res-1" {
		t.Fatalf("restore_id = %q", rid)
	}
}

// ---- TokenClient ------------------------------------------------------------

func TestTokenClient_SelfService(t *testing.T) {
	var paths, methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		methods = append(methods, r.Method)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	tc := NewTokenClient(newTestClient(srv, nil))
	ctx := context.Background()
	if _, err := tc.ListSelf(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.SelfAudit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.RotateSelf(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.RevokeSelf(ctx, "tok-1"); err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"/v1/auth/tokens/self", "/v1/auth/tokens/self/audit", "/v1/auth/tokens/self/rotate", "/v1/auth/tokens/self/tok-1"}
	wantMethods := []string{"GET", "GET", "POST", "DELETE"}
	for i := range wantPaths {
		if paths[i] != wantPaths[i] || methods[i] != wantMethods[i] {
			t.Fatalf("call %d = %s %s, want %s %s", i, methods[i], paths[i], wantMethods[i], wantPaths[i])
		}
	}
}

// ---- TenantAdminClient ----------------------------------------------------------

func TestTenantAdminClient_Me(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"tenant_id":"acme"}`)
	}))
	defer srv.Close()

	out, err := NewTenantAdminClient(newTestClient(srv, nil)).Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/tenants/me" || out["tenant_id"] != "acme" {
		t.Fatalf("path=%q out=%v", gotPath, out)
	}
}

// ---- McpClient --------------------------------------------------------------

func TestMcpClient_CallTool_Unwraps(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		inner := `{"tools":[{"name":"query_knowledge"}]}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}]}`, strconvQuote(inner))
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	mcp := NewMcpClient(c)
	out, err := mcp.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || gotPath != "/v1/mcp/tools" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if len(out) != 1 || out[0]["name"] != "query_knowledge" {
		t.Fatalf("out = %v", out)
	}
	_ = gotBody
}

// ---- ObjectClient (NDJSON) --------------------------------------------------

func TestObjectClient_Upsert(t *testing.T) {
	var gotPath, gotQuery, gotCT string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"object_id":"p1","write_seq":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "ingest"})
	oc := NewObjectClient(c)
	out, err := oc.Upsert(context.Background(), "Person", "p1", map[string]any{"name": "Alice"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/ingest" || !contains(gotQuery, "object_type=Person") || !contains(gotQuery, "purpose=ingest") {
		t.Fatalf("path=%q query=%q", gotPath, gotQuery)
	}
	if gotCT != "application/x-ndjson" {
		t.Fatalf("content-type = %q", gotCT)
	}
	if !contains(gotBody, `"id":"p1"`) || !contains(gotBody, `"name":"Alice"`) {
		t.Fatalf("body = %q", gotBody)
	}
	if out["object_id"] != "p1" {
		t.Fatalf("out = %v", out)
	}
}

// ---- A2AClient --------------------------------------------------------------

func TestA2AClient_SubmitTask(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		fmt.Fprint(w, `{"id":"task-1","status":"running"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	a := NewA2AClient(c)
	out, err := a.SubmitTask(context.Background(), map[string]any{"name": "summarise"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/v1/a2a/tasks" || out["id"] != "task-1" {
		t.Fatalf("method/path = %s %s, out = %v", gotMethod, gotPath, out)
	}
}

func TestA2AClient_AgentCard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/agent.json" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"name":"relata","version":"1.0"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	a := NewA2AClient(c)
	card, err := a.AgentCard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if card["name"] != "relata" {
		t.Fatalf("card = %v", card)
	}
}

// ---- IdentityClient ---------------------------------------------------------

func TestIdentityClient_EraseSubject(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"receipt":"ok"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	ic := NewIdentityClient(c)
	out, err := ic.EraseSubject(context.Background(), "alice@example.com", "gdpr-art-17", "erasure", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["receipt"] != "ok" {
		t.Fatalf("out = %v", out)
	}
	sql, _ := gotBody["sql"].(string)
	// Omitted certify (opts == nil) defaults to the safe side — no CERTIFY
	// keyword; the server then refuses the erasure until the caller opts in.
	if !contains(sql, "ERASE SUBJECT 'alice@example.com'") || contains(sql, "CERTIFY") {
		t.Fatalf("sql = %q", sql)
	}
}

func TestIdentityClient_EraseSubjectCertifyOptIn(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"receipt":"ok"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	ic := NewIdentityClient(c)
	if _, err := ic.EraseSubject(context.Background(), "alice@example.com", "gdpr-art-17", "erasure", &EraseSubjectOptions{Certify: true, CertifySet: true}); err != nil {
		t.Fatal(err)
	}
	sql, _ := gotBody["sql"].(string)
	if !contains(sql, "CERTIFY") {
		t.Fatalf("sql = %q, want CERTIFY when explicitly opted in", sql)
	}

	if _, err := ic.EraseSubject(context.Background(), "alice@example.com", "gdpr-art-17", "erasure", &EraseSubjectOptions{Certify: false, CertifySet: true}); err != nil {
		t.Fatal(err)
	}
	sql, _ = gotBody["sql"].(string)
	if contains(sql, "CERTIFY") {
		t.Fatalf("sql = %q, want CERTIFY omitted for Certify=false", sql)
	}
}

// ---- StreamingClient (NDJSON) ----------------------------------------------

func TestStreamingClient_QueryRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/query/stream" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/x-ndjson" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"_meta":{"query_id":"q-1","started_at":1,"columns":["id","name"]}}`)
		fmt.Fprintln(w, `{"id":1,"name":"A"}`)
		fmt.Fprintln(w, `{"id":2,"name":"B"}`)
		fmt.Fprintln(w, `{"_meta":{"processing_time_ms":1,"row_count":2,"truncated":false}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	s := NewStreamingClient(c)
	it, err := s.QueryRows(context.Background(), "SELECT * FROM Person", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	var ids []int
	for it.Next() {
		ids = append(ids, int(it.Row()["id"].(float64)))
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestStreamingClient_QueryRows_PurposeRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c := newTestClient(srv, nil) // no default purpose
	s := NewStreamingClient(c)
	_, err := s.QueryRows(context.Background(), "SELECT 1", nil)
	if !errors.Is(err, ErrPurposeRequired) {
		t.Fatalf("err = %v, want ErrPurposeRequired", err)
	}
}

// ---- S3Client ---------------------------------------------------------------

// ---- SystemClient -----------------------------------------------------------

// ---- IngestClient -----------------------------------------------------------

func TestIngestClient_BulkCSV(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		fmt.Fprint(w, `{"accepted":3,"rejected":0,"write_seq":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "ingest"})
	ic := NewIngestClient(c)
	out, err := ic.BulkCSV(context.Background(), "Person", "id,name\n1,A\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotCT != "text/csv" {
		t.Fatalf("content-type = %q", gotCT)
	}
	if out["accepted"].(float64) != 3 {
		t.Fatalf("out = %v", out)
	}
}

// ---- VectorClient -----------------------------------------------------------

func TestVectorClient_PurposeRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c := newTestClient(srv, nil) // no default purpose
	v := NewVectorClient(c)
	_, err := v.KNNSearch(context.Background(), "Person", "emb", []float64{0.1, 0.2}, 5, nil)
	if !errors.Is(err, ErrPurposeRequired) {
		t.Fatalf("err = %v, want ErrPurposeRequired", err)
	}
}

func TestVectorClient_HybridSearch(t *testing.T) {
	var gotSQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotSQL, _ = body["sql"].(string)
		fmt.Fprint(w, `{"rows":[],"query_id":"q1","elapsed_ms":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	_, err := v.HybridSearch(context.Background(), "Document", "fraud", &HybridSearchOptions{K: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := "HYBRID_SEARCH FROM Document QUERY 'fraud' LIMIT 10"
	if gotSQL != want {
		t.Fatalf("sql = %q, want %q", gotSQL, want)
	}
}

func TestVectorClient_HybridSearch_TuningClauses(t *testing.T) {
	var gotSQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotSQL, _ = body["sql"].(string)
		fmt.Fprint(w, `{"rows":[],"query_id":"q1","elapsed_ms":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "research"})
	v := NewVectorClient(c)
	w := [3]float64{0.3, 0.5, 0.2}
	_, err := v.HybridSearch(context.Background(), "Document", "o'reilly", &HybridSearchOptions{
		K: 5, Rerank: true, Metric: "cosine", Weights: &w,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `HYBRID_SEARCH FROM Document QUERY 'o\'reilly' LIMIT 5 RERANK METRIC cosine WEIGHTS 0.3 0.5 0.2`
	if gotSQL != want {
		t.Fatalf("sql = %q, want %q", gotSQL, want)
	}
}

func TestVectorClient_HybridSearch_RequiresTextOrEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	_, err := v.HybridSearch(context.Background(), "Document", "", nil)
	if err == nil {
		t.Fatal("expected error when query_text is empty")
	}
}

func TestVectorClient_SimilarTo(t *testing.T) {
	var gotSQL string
	var gotParams []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotSQL, _ = body["sql"].(string)
		if p, ok := body["params"].([]any); ok {
			gotParams = p
		}
		fmt.Fprint(w, `{"rows":[],"query_id":"q1","elapsed_ms":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	_, err := v.SimilarTo(context.Background(), "Document", "d-1", &SimilarToOptions{K: 3})
	if err != nil {
		t.Fatal(err)
	}
	// #3211: the reference id is bound as a server-side $1 parameter.
	want := "SIMILAR TO Document WHERE id = $1 LIMIT 3"
	if gotSQL != want {
		t.Fatalf("sql = %q, want %q", gotSQL, want)
	}
	if len(gotParams) != 1 || gotParams[0] != "d-1" {
		t.Fatalf("params = %v, want [d-1]", gotParams)
	}
}

func TestVectorClient_Embed(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"embedding":[0.1,0.2],"model":"lexical","dim":2}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	v := NewVectorClient(c)
	resp, err := v.Embed(context.Background(), "Alice Smith", "")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/embed" {
		t.Fatalf("path = %q, want /v1/embed", gotPath)
	}
	if _, hasModel := gotBody["model"]; hasModel {
		t.Fatalf("model should be omitted when empty, got body = %v", gotBody)
	}
	if resp.Dim != 2 || resp.Model != "lexical" || len(resp.Embedding) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestVectorClient_EmbedBatch(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embed" {
			t.Errorf("path = %q, want /v1/embed", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"embeddings":[[0.1],[0.2]],"model":"lexical","dim":1,"count":2}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	v := NewVectorClient(c)
	resp, err := v.EmbedBatch(context.Background(), []string{"Alice", "Bob"}, "lexical")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "lexical" {
		t.Fatalf("body = %v", gotBody)
	}
	if resp.Count != 2 || len(resp.Embeddings) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

// ---- SearchClient (typed /v1/search JSON query door, #1983/#2169) -------------

func TestSearchClient_Query(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"rows":[{"id":"doc-1"}],"query_id":"q1","elapsed_ms":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	s := NewSearchClient(c)
	result, err := s.Query(context.Background(), SearchRequest{
		From: "Document",
		Text: "fraud pattern",
		Filters: []SearchFilter{
			{Field: "status", Op: "eq", Value: "open"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0]["id"] != "doc-1" {
		t.Fatalf("rows = %v", result.Rows)
	}
	if gotBody["from"] != "Document" {
		t.Fatalf("body = %v", gotBody)
	}
	rankBy, _ := gotBody["rank_by"].([]any)
	if len(rankBy) != 3 || rankBy[0] != "bm25" || rankBy[2] != "fraud pattern" {
		t.Fatalf("rank_by = %v", gotBody["rank_by"])
	}
}

func TestSearchClient_Query_QueryVector(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"rows":[],"query_id":"q1","elapsed_ms":1}`)
	}))
	defer srv.Close()

	s := NewSearchClient(newTestClient(srv, nil))
	_, err := s.Query(context.Background(), SearchRequest{
		From:             "Document",
		RankBy:           []any{"vector", "ann", "fraud"},
		QueryVector:      []float32{0.5, 0.25},
		QueryVectorModel: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	qv, _ := gotBody["query_vector"].([]any)
	if len(qv) != 2 || qv[0] != 0.5 || gotBody["query_vector_model"] != "m1" {
		t.Fatalf("body = %v", gotBody)
	}
	if _, err := s.Query(context.Background(), SearchRequest{
		From: "Document", QueryVectorModel: "m1",
	}); err == nil {
		t.Fatal("model without vector must error")
	}
}

func TestSearchClient_Query_ComputeAttributes(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"rows":[{"id":"doc-1"}],"query_id":"q1","elapsed_ms":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	s := NewSearchClient(c)
	_, err := s.Query(context.Background(), SearchRequest{
		From:              "Document",
		Text:              "fraud pattern",
		ComputeAttributes: map[string]string{"bm25_score": "BM25()"},
	})
	if err != nil {
		t.Fatal(err)
	}
	computeAttrs, _ := gotBody["compute_attributes"].(map[string]any)
	if computeAttrs["bm25_score"] != "BM25()" {
		t.Fatalf("compute_attributes = %v", gotBody["compute_attributes"])
	}
}
