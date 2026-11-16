package relata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #5696: the SDK surfaces an ACL denial and a tenant refusal, never an empty
// success. Wire bodies are the shared fixtures forbidden_403_acl_denied and
// forbidden_403_org_not_entitled, replayed against the live binary by the Rust
// test http_e2e::contract_matrix::problem_fixtures_match_live_server. Each
// high-level call on the probed surfaces (query, alerts, audit log, lookups,
// quarantine) must return an error wrapping ErrForbidden with the server's
// code; a nil error with an empty result would hide the refusal.
func TestTenantAndPolicyRefusalsAreErrors(t *testing.T) {
	fx := loadProblemFixtures(t)
	byName := map[string]problemFixtureCase{}
	for _, c := range fx.Cases {
		byName[c.Name] = c
	}
	for _, tc := range []struct{ fixture, tenant string }{
		{"forbidden_403_acl_denied", ""},
		{"forbidden_403_org_not_entitled", "tenant-b"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			c, ok := byName[tc.fixture]
			if !ok {
				t.Fatalf("fixture %s missing", tc.fixture)
			}
			var sent []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent = append(sent, r.Header.Get(headerTenant))
				for k, v := range c.Wire.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.Wire.Status)
				_, _ = w.Write(c.Wire.Body)
			}))
			defer srv.Close()
			client := newTestClient(srv, &ClientOptions{
				BearerToken:    "t",
				DefaultPurpose: "investigation",
				Tenant:         tc.tenant,
			})
			ctx := context.Background()
			gov := NewGovernanceClient(client)
			audit := NewAuditClient(client)
			identity := NewIdentityClient(client)
			ingest := NewIngestControlClient(client)
			calls := []struct {
				name string
				run  func() (any, error)
			}{
				{"Query", func() (any, error) { return client.Query(ctx, "SELECT * FROM ProbeEvt") }},
				{"ListAlerts", func() (any, error) { return gov.ListAlerts(ctx, nil) }},
				{"AuditLog", func() (any, error) { return audit.Log(ctx, nil) }},
				{"AuditCount", func() (any, error) { return audit.Count(ctx) }},
				{"ListLookups", func() (any, error) { return identity.ListLookups(ctx) }},
				{"InvokeLookup", func() (any, error) { return identity.InvokeLookup(ctx, "hosts", "10.0.0.1") }},
				{"ListQuarantine", func() (any, error) { return ingest.ListQuarantine(ctx, nil) }},
			}
			for _, call := range calls {
				got, err := call.run()
				if !errors.Is(err, ErrForbidden) {
					t.Fatalf("%s: returned (%v, %v), want an error wrapping ErrForbidden", call.name, got, err)
				}
				var re *RelataError
				if !errors.As(err, &re) || re.StatusCode != 403 || re.Code != optString(c.Expect.Code) || re.Retryable {
					t.Fatalf("%s: %+v does not carry the server's 403 %q", call.name, re, optString(c.Expect.Code))
				}
			}
			if len(sent) != len(calls) {
				t.Fatalf("%d requests for %d calls", len(sent), len(calls))
			}
			for _, h := range sent {
				if h != tc.tenant {
					t.Fatalf("tenant header %q, want %q", h, tc.tenant)
				}
			}
		})
	}
}
