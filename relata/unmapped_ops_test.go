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

// Wire tests for server operations that previously had no SDK method (Refs #5960).

type unmappedCall struct {
	method string
	uri    string
	body   string
}

func unmappedServer(t *testing.T, contentType, body string) (*httptest.Server, *[]unmappedCall) {
	t.Helper()
	var calls []unmappedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, unmappedCall{r.Method, r.RequestURI, string(b)})
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func expectUnmappedCalls(t *testing.T, got []unmappedCall, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d calls %v, want %d %v", len(got), got, len(want), want)
	}
	for i, w := range want {
		if g := got[i].method + " " + got[i].uri; g != w {
			t.Fatalf("call %d = %q, want %q", i, g, w)
		}
	}
}

func TestSystemClient_UnmappedOps(t *testing.T) {
	srv, calls := unmappedServer(t, "application/json", `{}`)
	s := NewSystemClient(newTestClient(srv, &ClientOptions{DefaultPurpose: "ops"}))
	ctx := context.Background()
	for _, err := range []error{
		errOnly(s.SystemConfig(ctx)),
		errOnly(s.SystemSpecs(ctx)),
		errOnly(s.MCPSessions(ctx, nil)),
		errOnly(s.MCPSessions(ctx, &MCPSessionsOptions{SessionID: "s 1", Limit: 5})),
		errOnly(s.UpdateNotificationRule(ctx, "r/1", map[string]any{"enabled": false})),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	expectUnmappedCalls(t, *calls, []string{
		"GET /v1/system/config",
		"GET /v1/system/specs",
		"GET /v1/mcp/sessions",
		"GET /v1/mcp/sessions?limit=5&session_id=s+1",
		"PATCH /v1/notification-rules/r%2F1",
	})
	var patch map[string]any
	if err := json.Unmarshal([]byte((*calls)[4].body), &patch); err != nil || patch["enabled"] != false {
		t.Fatalf("patch body = %q (%v)", (*calls)[4].body, err)
	}
}

func errOnly(_ map[string]any, err error) error { return err }

func TestGovernanceClient_UnmappedOps(t *testing.T) {
	srv, calls := unmappedServer(t, "application/json", `{}`)
	g := NewGovernanceClient(newTestClient(srv, &ClientOptions{DefaultPurpose: "ops"}))
	ctx := context.Background()
	if _, err := g.GetRule(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.RemoveRuleException(ctx, "a/b", "e 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetRetentionPolicy(ctx, "TraceSpan", 30, ""); err != nil {
		t.Fatal(err)
	}
	expectUnmappedCalls(t, *calls, []string{
		"GET /v1/rules/a%2Fb",
		"DELETE /v1/rules/a%2Fb/exceptions/e%201",
		"POST /v1/retention/policies",
	})
	var body map[string]any
	_ = json.Unmarshal([]byte((*calls)[2].body), &body)
	if body["object_type"] != "TraceSpan" || body["max_age_days"] != float64(30) || body["action"] != "delete" {
		t.Fatalf("body = %v", body)
	}
}

func TestIngestClient_ImportSTIX(t *testing.T) {
	srv, calls := unmappedServer(t, "application/json", `{}`)
	i := NewIngestClient(newTestClient(srv, &ClientOptions{DefaultPurpose: "intel"}))
	bundle := map[string]any{"type": "bundle", "id": "bundle--1"}
	if _, err := i.ImportSTIX(context.Background(), bundle, nil); err != nil {
		t.Fatal(err)
	}
	expectUnmappedCalls(t, *calls, []string{"POST /v1/ingest/import?format=stix&purpose=intel"})
	var got map[string]any
	_ = json.Unmarshal([]byte((*calls)[0].body), &got)
	if got["id"] != "bundle--1" {
		t.Fatalf("body = %v", got)
	}
}

func TestClient_TypeDetectConfig(t *testing.T) {
	srv, calls := unmappedServer(t, "application/json", `{}`)
	c := newTestClient(srv, nil)
	ctx := context.Background()
	if _, err := c.ListTypeDetectConfigs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetTypeDetectConfig(ctx, "Person", []string{"contact"}); err != nil {
		t.Fatal(err)
	}
	expectUnmappedCalls(t, *calls, []string{"GET /v1/types/detect-config", "POST /v1/types/detect-config"})
	var got map[string]any
	_ = json.Unmarshal([]byte((*calls)[1].body), &got)
	if got["type"] != "Person" {
		t.Fatalf("body = %v", got)
	}
}

func TestStreamingClient_GraphChangesAndA2AStream(t *testing.T) {
	srv, calls := unmappedServer(t, "text/event-stream", "event: change\ndata: {\"id\":1}\n\n")
	st := NewStreamingClient(newTestClient(srv, &ClientOptions{DefaultPurpose: "ops"}))
	ctx := context.Background()
	no := false
	it, err := st.GraphChanges(ctx, &GraphChangesOptions{Type: "Person", Reconnect: &no})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for it.Next() {
		n++
		if it.Event().Type != "change" || it.Event().Data["id"] != float64(1) {
			t.Fatalf("event = %+v", it.Event())
		}
	}
	it.Close()
	it2, err := st.A2ATaskStream(ctx, "t/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for it2.Next() {
		n++
	}
	it2.Close()
	if n != 2 {
		t.Fatalf("events = %d, want 2", n)
	}
	expectUnmappedCalls(t, *calls, []string{
		"GET /v1/graph/changes?type=Person",
		"GET /v1/a2a/tasks/t%2F1/stream",
	})
	it3, err := st.GraphChanges(ctx, &GraphChangesOptions{Reconnect: &no})
	if err != nil {
		t.Fatal(err)
	}
	it3.Close()
	if got := (*calls)[2].uri; got != "/v1/graph/changes" {
		t.Fatalf("unfiltered uri = %q", got)
	}
}
