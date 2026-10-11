package relata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #723 — jobs SDK surface.

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
