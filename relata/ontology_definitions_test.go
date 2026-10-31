package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestOntologyDefinitionsRoutesAndBodies(t *testing.T) {
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
	o := NewOntologyDefinitionsClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	ctx := context.Background()
	d := map[string]any{"objects": []any{map[string]any{"name": "Person"}}}
	calls := []func() error{
		func() error { _, e := o.List(ctx); return e },
		func() error { _, e := o.Create(ctx, "crm", d); return e },
		func() error { _, e := o.Get(ctx, "crm"); return e },
		func() error { _, e := o.SaveDraft(ctx, "crm", d); return e },
		func() error { _, e := o.Validate(ctx, "crm", nil); return e },
		func() error { _, e := o.Validate(ctx, "crm", d); return e },
		func() error { _, e := o.Compare(ctx, "crm", "1", "draft"); return e },
		func() error { _, e := o.Compare(ctx, "crm", "", ""); return e },
		func() error {
			_, e := o.Publish(ctx, "crm", PublishOptions{ExpectedVersion: 1, AcceptMigration: true, FromVersion: 1})
			return e
		},
		func() error { _, e := o.Publish(ctx, "crm", PublishOptions{}); return e },
		func() error { _, e := o.Version(ctx, "crm", 2); return e },
		func() error { _, e := o.Status(ctx, "crm"); return e },
		func() error { _, e := o.Migration(ctx, "crm", 2); return e },
		func() error { _, e := o.RunMigration(ctx, "crm", 2, 10, true); return e },
		func() error { _, e := o.RunMigration(ctx, "crm", 2, 0, false); return e },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	nb := map[string]any(nil)
	want := [][]any{
		{"GET", "/v1/ontology/definitions", "", nb},
		{"POST", "/v1/ontology/definitions", "", map[string]any{"name": "crm", "definition": d}},
		{"GET", "/v1/ontology/definitions/crm", "", nb},
		{"PUT", "/v1/ontology/definitions/crm/draft", "", map[string]any{"definition": d}},
		{"POST", "/v1/ontology/definitions/crm/validate", "", map[string]any{}},
		{"POST", "/v1/ontology/definitions/crm/validate", "", map[string]any{"definition": d}},
		{"GET", "/v1/ontology/definitions/crm/compare", "from=1&to=draft", nb},
		{"GET", "/v1/ontology/definitions/crm/compare", "", nb},
		{"POST", "/v1/ontology/definitions/crm/publish", "", map[string]any{"expected_version": float64(1), "accept_migration": true, "from_version": float64(1)}},
		{"POST", "/v1/ontology/definitions/crm/publish", "", map[string]any{"expected_version": float64(0)}},
		{"GET", "/v1/ontology/definitions/crm/versions/2", "", nb},
		{"GET", "/v1/ontology/definitions/crm/status", "", nb},
		{"GET", "/v1/ontology/definitions/crm/migrations/2", "", nb},
		{"POST", "/v1/ontology/definitions/crm/migrations/2/run", "", map[string]any{"max_rows": float64(10), "retry_failed": true}},
		{"POST", "/v1/ontology/definitions/crm/migrations/2/run", "", map[string]any{}},
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("got %#v\nwant %#v", seen, want)
	}
	if _, err := o.Get(ctx, "a/b c"); err != nil || seen[len(seen)-1][1] != "/v1/ontology/definitions/a%2Fb%20c" {
		t.Fatalf("name not path-escaped: %v %v", seen[len(seen)-1], err)
	}
	status = http.StatusPreconditionFailed
	if _, err := o.Publish(ctx, "crm", PublishOptions{ExpectedVersion: 1}); err == nil {
		t.Fatal("a 412 must be an error, never a success")
	}
}
