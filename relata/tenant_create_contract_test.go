package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Assert the actual request body: mocks returning successful tenant records
// previously hid the unsupported tenant_id request field (#5652).
func TestTenantCreateWireContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts *CreateTenantOptions
		want map[string]any
	}{
		{"defaults", nil, map[string]any{"id": "acme", "tier": "standard"}},
		{"options", &CreateTenantOptions{Tier: "enterprise", Fields: map[string]any{"name": "Acme"}}, map[string]any{"id": "acme", "tier": "enterprise", "name": "Acme"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/platform/tenants" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(body, tc.want) {
					t.Errorf("request = %#v, want %#v", body, tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"acme"}`))
			}))
			defer srv.Close()
			if _, err := NewTenantAdminClient(newTestClient(srv, nil)).Create(context.Background(), "acme", tc.opts); err != nil {
				t.Fatal(err)
			}
		})
	}
}
