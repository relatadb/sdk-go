package relata

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListWormPoliciesReadsPolicies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"policies":[{"object_type":"Audit","worm":true,"retention":"indefinite","retention_secs":null},{"object_type":"Ledger","worm":true,"retention":"indefinite","retention_secs":null}],"worm_types":["Audit","Ledger"],"total":2}`))
	}))
	defer srv.Close()
	got, err := NewGovernanceClient(New(srv.URL, &ClientOptions{BearerToken: "x"})).ListWormPolicies(context.Background())
	if err != nil || len(got) != 2 || got[0]["object_type"] != "Audit" || got[0]["retention"] != "indefinite" || got[1]["object_type"] != "Ledger" {
		t.Fatalf("got %v err %v", got, err)
	}
}

func TestListWormPoliciesReadsWormTypesOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"worm_types":["Ledger"],"total":1}`))
	}))
	defer srv.Close()
	got, err := NewGovernanceClient(New(srv.URL, &ClientOptions{BearerToken: "x"})).ListWormPolicies(context.Background())
	if err != nil || len(got) != 1 || got[0]["object_type"] != "Ledger" || got[0]["worm"] != true {
		t.Fatalf("got %v err %v", got, err)
	}
}

func TestSetWormPolicyRetentionIsOptional(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"object_type":"A","worm":true}`))
	}))
	defer srv.Close()
	g := NewGovernanceClient(New(srv.URL, &ClientOptions{BearerToken: "x"}))
	if _, err := g.SetWormPolicy(context.Background(), "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetWormPolicy(context.Background(), "A", 60); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || len(bodies[0]) != 0 || bodies[1]["retention_secs"] != float64(60) {
		t.Fatalf("bodies = %v", bodies)
	}
}
