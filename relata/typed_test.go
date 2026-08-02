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
	if gotPath != "/rules" || gotQuery != "object_type=Person" {
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
	out, err := g.CreateRule(context.Background(), map[string]any{"name": "x", "object_type": "Person"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/rules" || gotQuery != "purpose=audit" {
		t.Fatalf("method/path/query = %s %s %s", gotMethod, gotPath, gotQuery)
	}
	if gotBody["name"] != "x" {
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

func TestGovernanceClient_ImportSigma(t *testing.T) {
	var gotPath, gotQuery, gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"rules_imported":1,"rules_skipped":0,"errors":[]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "audit"})
	g := NewGovernanceClient(c)
	yaml := "title: test\ndetection:\n  condition: selection\n"
	out, err := g.ImportSigma(context.Background(), yaml, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/rules/sigma" || gotQuery != "purpose=audit" {
		t.Fatalf("path/query = %s %s", gotPath, gotQuery)
	}
	if gotContentType != "application/x-yaml" {
		t.Fatalf("content-type = %s", gotContentType)
	}
	if gotBody != yaml {
		t.Fatalf("body = %q, want raw YAML %q", gotBody, yaml)
	}
	if out["rules_imported"] != float64(1) {
		t.Fatalf("out = %v", out)
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
	if gotMethod != "DELETE" || gotPath != "/rules/r1" {
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
	if gotMethod != "GET" || gotPath != "/gdpr/dsar" {
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
	if gotMethod != "PATCH" || gotPath != "/alerts/update/a1" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
}

// ---- AuditClient ------------------------------------------------------------

func TestAuditClient_Entries(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, `{"entries":[{"id":1}],"next_cursor":"c1","chain_valid":true}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	a := NewAuditClient(c)
	out, err := a.Entries(context.Background(), &AuditEntriesOptions{Principal: "bob", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(gotQuery, "principal=bob") || !contains(gotQuery, "limit=50") {
		t.Fatalf("query = %q", gotQuery)
	}
	if out["next_cursor"] != "c1" {
		t.Fatalf("out = %v", out)
	}
}

func TestAuditClient_FindByRequestID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"entries":[{"id":1,"request_id":"rid"}],"chain_valid":true}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	a := NewAuditClient(c)
	got, err := a.FindByRequestID(context.Background(), "rid")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got["request_id"] != "rid" {
		t.Fatalf("got = %v", got)
	}
}

func TestAuditClient_ExportPDF(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4 binary blob"))
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	a := NewAuditClient(c)
	pdf, err := a.ExportPDF(context.Background(), "case-42", "")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/report/pdf" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(pdf) == 0 || pdf[0] != '%' {
		t.Fatalf("pdf = %v", pdf)
	}
}

// ---- TenantAdminClient ------------------------------------------------------

func TestTenantAdminClient_CRUD(t *testing.T) {
	var paths []string
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		methods = append(methods, r.Method)
		switch r.URL.Path {
		case "/platform/tenants":
			fmt.Fprint(w, `{"tenant_id":"acme","tier":"standard"}`)
		case "/platform/tenants/acme":
			fmt.Fprint(w, `{"tenant_id":"acme"}`)
		case "/platform/tenants/acme/tier":
			fmt.Fprint(w, `{"tenant_id":"acme","tier":"enterprise"}`)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	tc := NewTenantAdminClient(c)
	if _, err := tc.Create(context.Background(), "acme", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Get(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.SetTier(context.Background(), "acme", "enterprise"); err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"/platform/tenants", "/platform/tenants/acme", "/platform/tenants/acme/tier"}
	wantMethods := []string{"POST", "GET", "PATCH"}
	for i := range wantPaths {
		if paths[i] != wantPaths[i] {
			t.Fatalf("path[%d] = %q, want %q", i, paths[i], wantPaths[i])
		}
		if methods[i] != wantMethods[i] {
			t.Fatalf("method[%d] = %q, want %q", i, methods[i], wantMethods[i])
		}
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

func TestTokenClient_Remember(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		fmt.Fprint(w, `{"newly_inserted":true}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	tc := NewTokenClient(c)
	inserted, err := tc.Remember(context.Background(), "tok-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("expected newly_inserted=true")
	}
	if gotPath != "/tokens" || gotMethod != "POST" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
}

func TestTokenClient_Stats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"count":42,"oldest_ns":123,"eviction_policy":"lru"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	tc := NewTokenClient(c)
	stats, err := tc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 42 || stats.OldestNS != 123 || stats.EvictionPolicy != "lru" {
		t.Fatalf("stats = %+v", stats)
	}
}

// ---- LogClient --------------------------------------------------------------

func TestLogClient_AppendAndHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/log/leaf":
			fmt.Fprint(w, `{"index":5,"commit_ns":999}`)
		case "/log/head":
			fmt.Fprint(w, `{"head":5}`)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	lc := NewLogClient(c)
	idx, commit, err := lc.Append(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if idx != 5 || commit != 999 {
		t.Fatalf("idx=%d commit=%d", idx, commit)
	}
	head, err := lc.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if head != 5 {
		t.Fatalf("head = %d", head)
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
	if gotMethod != "GET" || gotPath != "/mcp/tools" {
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
	if gotPath != "/ingest" || !contains(gotQuery, "object_type=Person") || !contains(gotQuery, "purpose=ingest") {
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
	if gotMethod != "POST" || gotPath != "/a2a/tasks" || out["id"] != "task-1" {
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
	if !contains(sql, "ERASE SUBJECT 'alice@example.com'") || !contains(sql, "CERTIFY") {
		t.Fatalf("sql = %q", sql)
	}
}

// ---- StreamingClient (NDJSON) ----------------------------------------------

func TestStreamingClient_QueryRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/query" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"id":1,"name":"A"}`)
		fmt.Fprintln(w, `{"id":2,"name":"B"}`)
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

func TestS3Client_FromClient(t *testing.T) {
	c := New("http://localhost:9090", &ClientOptions{BearerToken: "tok", Tenant: "acme"})
	s3 := NewS3Client(c)
	if s3.EndpointURL != "http://localhost:9090" {
		t.Fatalf("endpoint = %q", s3.EndpointURL)
	}
	if s3.BearerToken != "tok" || s3.Tenant != "acme" || s3.Region != "us-east-1" {
		t.Fatalf("s3 = %+v", s3)
	}
	hc := s3.HTTP(nil)
	if hc == nil || hc.Timeout == 0 {
		t.Fatalf("http client = %+v", hc)
	}
}

// ---- SystemClient -----------------------------------------------------------

func TestSystemClient_LLMConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/llm" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"endpoint":"http://llm","models":["gpt"]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	sc := NewSystemClient(c)
	out, err := sc.LLMConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["endpoint"] != "http://llm" {
		t.Fatalf("out = %v", out)
	}
}

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

// ---- SystemClient jobs + workflows (#723) -----------------------------------

func TestSystemClient_JobsAndWorkflows(t *testing.T) {
	var gotPaths []string
	var gotMethods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		gotMethods = append(gotMethods, r.Method)
		switch r.URL.Path {
		case "/jobs/c2_beacon_detect":
			fmt.Fprint(w, `{"name":"c2_beacon_detect","status":"ok"}`)
		case "/workflows":
			if r.Method == "GET" {
				fmt.Fprint(w, `{"workflows":["demo_flow"]}`)
			} else {
				fmt.Fprint(w, `{"name":"demo_flow"}`)
			}
		case "/workflows/demo_flow":
			fmt.Fprint(w, `{"name":"demo_flow","steps":[]}`)
		case "/workflows/demo_flow/run":
			fmt.Fprint(w, `{"run_id":"run-1","status":"running"}`)
		case "/workflows/demo_flow/status":
			fmt.Fprint(w, `{"run_id":"run-1","status":"ok"}`)
		case "/workflows/runs/run-1":
			fmt.Fprint(w, `{"run_id":"run-1","workflow_name":"demo_flow","status":"ok","attempt_count":2,"last_error":null,"steps":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	sys := NewSystemClient(c)
	ctx := context.Background()

	js, err := sys.JobStatus(ctx, "c2_beacon_detect")
	if err != nil {
		t.Fatal(err)
	}
	if js["name"] != "c2_beacon_detect" {
		t.Fatalf("job_status = %v", js)
	}

	wfs, err := sys.ListWorkflows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if wfs["workflows"] == nil {
		t.Fatalf("list_workflows = %v", wfs)
	}

	if _, err := sys.RegisterWorkflow(ctx, "demo_flow", []map[string]any{{"name": "s1"}}); err != nil {
		t.Fatal(err)
	}

	def, err := sys.GetWorkflow(ctx, "demo_flow")
	if err != nil {
		t.Fatal(err)
	}
	if def["name"] != "demo_flow" {
		t.Fatalf("get_workflow = %v", def)
	}

	run, err := sys.RunWorkflow(ctx, "demo_flow")
	if err != nil {
		t.Fatal(err)
	}
	if run["run_id"] != "run-1" {
		t.Fatalf("run_workflow = %v", run)
	}

	st, err := sys.WorkflowStatus(ctx, "demo_flow")
	if err != nil {
		t.Fatal(err)
	}
	if st["status"] != "ok" {
		t.Fatalf("workflow_status = %v", st)
	}

	detail, err := sys.WorkflowRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.AttemptCount != 2 || detail.RunID != "run-1" {
		t.Fatalf("workflow_run = %+v", detail)
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
	_, err := v.HybridSearch(context.Background(), "Document", "fraud", nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM HYBRID_SEARCH(from => 'Document', limit => 10, query_text => 'fraud')"
	if gotSQL != want {
		t.Fatalf("sql = %q, want %q", gotSQL, want)
	}
}

func TestVectorClient_HybridSearch_RequiresTextOrEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	v := NewVectorClient(c)
	_, err := v.HybridSearch(context.Background(), "Document", "", nil, "", nil)
	if err == nil {
		t.Fatal("expected error when neither query_text nor query_embedding is set")
	}
}

func TestVectorClient_SimilarTo(t *testing.T) {
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
	_, err := v.SimilarTo(context.Background(), "Document", "d-1", &SimilarToOptions{K: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM SIMILAR TO Document WHERE id = 'd-1' LIMIT 3"
	if gotSQL != want {
		t.Fatalf("sql = %q, want %q", gotSQL, want)
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
	if gotPath != "/embed" {
		t.Fatalf("path = %q, want /embed", gotPath)
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
		if r.URL.Path != "/embed/batch" {
			t.Errorf("path = %q, want /embed/batch", r.URL.Path)
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

// ---- SearchClient (typed /search JSON query door, #1983/#2169) -------------

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
