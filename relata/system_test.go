package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #723 — jobs/workflows SDK surface.

func TestSystemClient_JobStatus(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		fmt.Fprint(w, `{"name":"c2_beacon","status":"Running"}`)
	}))
	defer srv.Close()

	s := NewSystemClient(newTestClient(srv, &ClientOptions{DefaultPurpose: "ops"}))
	got, err := s.JobStatus(context.Background(), "c2_beacon")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/jobs/c2_beacon" || gotMethod != "GET" {
		t.Fatalf("path=%q method=%q", gotPath, gotMethod)
	}
	if got["status"] != "Running" {
		t.Fatalf("status=%v", got["status"])
	}
}

func TestSystemClient_RunWorkflow(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		fmt.Fprint(w, `{"run_id":"enrich/abc-123","status":"Running"}`)
	}))
	defer srv.Close()

	s := NewSystemClient(newTestClient(srv, nil))
	got, err := s.RunWorkflow(context.Background(), "enrich")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/workflows/enrich/run" || gotMethod != "POST" {
		t.Fatalf("path=%q method=%q", gotPath, gotMethod)
	}
	if got["run_id"] != "enrich/abc-123" {
		t.Fatalf("run_id=%v", got["run_id"])
	}
}

func TestSystemClient_WorkflowRun_surfacesRetryFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workflows/runs/enrich/abc-123" || r.Method != "GET" {
			t.Fatalf("path=%q method=%q", r.URL.Path, r.Method)
		}
		// #711/#713 made attempt_count + last_error accurate — SDK must surface them.
		fmt.Fprint(w, `{"run_id":"enrich/abc-123","workflow_name":"enrich","status":"Failed","attempt_count":3,"last_error":"embed timeout","steps":[]}`)
	}))
	defer srv.Close()

	s := NewSystemClient(newTestClient(srv, nil))
	got, err := s.WorkflowRun(context.Background(), "enrich/abc-123")
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptCount != 3 || got.LastError == nil || *got.LastError != "embed timeout" {
		t.Fatalf("attempt=%d lastError=%v", got.AttemptCount, got.LastError)
	}
}

func TestSystemClient_RegisterWorkflow_postsSteps(t *testing.T) {
	var gotPath, gotMethod string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"name":"wf"}`)
	}))
	defer srv.Close()

	steps := []map[string]any{{"name": "q1", "kind": "query", "sql": "SELECT 1"}}
	s := NewSystemClient(newTestClient(srv, nil))
	_, err := s.RegisterWorkflow(context.Background(), "wf", steps)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/workflows" || gotMethod != "POST" {
		t.Fatalf("path=%q method=%q", gotPath, gotMethod)
	}
	if body["name"] != "wf" {
		t.Fatalf("body=%v", body)
	}
}
