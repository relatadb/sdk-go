package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFreshnessStatusAndBarrierWireShapes(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped" {
			t.Errorf("auth not inherited: %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/freshness/status":
			if r.Method != http.MethodGet {
				t.Errorf("status method %s", r.Method)
			}
			w.Write([]byte(`{"token":"fv1.x","indexes":[]}`))
		case "/freshness/barrier":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			w.Write([]byte(`{"freshness":{"status":"complete"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	f := NewFreshnessClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped"}))
	st, err := f.Status(context.Background())
	if err != nil || st["token"] != "fv1.x" {
		t.Fatalf("status: %v %v", st, err)
	}
	if _, err := f.Barrier(context.Background(), "fv1.t", &FreshnessOptions{Indexes: []string{"lexical", "vector"}, WaitMS: 250, RequireDurable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Barrier(context.Background(), "fv1.t", nil); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"token": "fv1.t", "indexes": []any{"lexical", "vector"}, "wait_ms": float64(250), "require_durable": true},
		{"token": "fv1.t"},
	}
	if !reflect.DeepEqual(bodies, want) {
		t.Fatalf("bodies %v want %v", bodies, want)
	}
}

func TestFreshnessNotReadyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(503)
		w.Write([]byte(`{"code":"freshness-not-ready","status":503,"title":"x","type":"t"}`))
	}))
	defer srv.Close()
	f := NewFreshnessClient(newTestClient(srv, &ClientOptions{MaxRetries: 0}))
	if out, err := f.Barrier(context.Background(), "fv1.t", &FreshnessOptions{WaitMS: 1}); err == nil {
		t.Fatalf("503 must not be success: %v", out)
	}
}

func TestFreshnessHeaders(t *testing.T) {
	if got := FreshnessHeaders("tok", nil); !reflect.DeepEqual(got, map[string]string{"X-Relata-Min-Version": "tok"}) {
		t.Fatal(got)
	}
	got := FreshnessHeaders("tok", &FreshnessOptions{Indexes: []string{"vector"}, WaitMS: 50, RequireDurable: true, Partial: true})
	want := map[string]string{
		"X-Relata-Min-Version": "tok", "X-Relata-Require-Index": "vector", "X-Relata-Wait-Ms": "50",
		"X-Relata-Require-Durable": "true", "X-Relata-Freshness-Mode": "partial",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}
