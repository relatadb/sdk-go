package relata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListWormPoliciesDecodesWormTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"worm_types":["Ledger","Audit"],"total":2}`))
	}))
	defer srv.Close()
	got, err := NewGovernanceClient(New(srv.URL, &ClientOptions{BearerToken: "x"})).ListWormPolicies(context.Background())
	if err != nil || len(got) != 2 || got[0]["object_type"] != "Ledger" || got[1]["object_type"] != "Audit" {
		t.Fatalf("got %v err %v", got, err)
	}
}
