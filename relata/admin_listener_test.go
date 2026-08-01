package relata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #2321 (ADR-0261): /admin/* and /platform/* are mounted ONLY on the
// loopbound admin control-plane listener, never the main data-plane
// listener. These tests pin two things:
//
//  1. ClientOptions.AdminBaseURL, when set, routes admin/platform-only
//     requests to the admin listener instead of BaseURL (spot-checked through
//     the real BackupClient/TenantAdminClient typed wrappers, not just the
//     raw Client).
//  2. A bare, detail-free 404 against an admin/platform-only path — the
//     shape the server's own RFC 7807 normalisation leaves behind for a
//     request that matched no route on the listener that answered — surfaces
//     a "you're probably pointed at the wrong port" hint instead of an
//     uninformative bare 404.

func TestIsAdminOnlyPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/admin/backup", true},
		{"/platform/tenants", true},
		{"/platform/tenants/acme/tier", true},
		{"/query", false},
		{"/tenants/me", false},
		{"/administer", false}, // no trailing slash after "admin"
	}
	for _, tc := range cases {
		if got := isAdminOnlyPath(tc.path); got != tc.want {
			t.Errorf("isAdminOnlyPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestEffectiveBaseURL_FallsBackToBaseURLWhenAdminUnset(t *testing.T) {
	c := New("http://localhost:9090", nil)
	if got := c.effectiveBaseURL("/admin/backup"); got != "http://localhost:9090" {
		t.Errorf("effectiveBaseURL(/admin/backup) = %q, want data-plane base_url", got)
	}
}

func TestEffectiveBaseURL_RoutesAdminPathsWhenConfigured(t *testing.T) {
	c := New("http://localhost:9090", &ClientOptions{AdminBaseURL: "http://localhost:9091/"})
	if got := c.effectiveBaseURL("/admin/backup"); got != "http://localhost:9091" {
		t.Errorf("effectiveBaseURL(/admin/backup) = %q, want admin listener", got)
	}
	if got := c.effectiveBaseURL("/platform/tenants"); got != "http://localhost:9091" {
		t.Errorf("effectiveBaseURL(/platform/tenants) = %q, want admin listener", got)
	}
	if got := c.effectiveBaseURL("/query"); got != "http://localhost:9090" {
		t.Errorf("effectiveBaseURL(/query) = %q, want data-plane base_url unaffected", got)
	}
}

// TestAdminBaseURL_BackupAndTenantAdminClientsRouteToAdminListener spot-checks
// (issue acceptance criterion) that BackupClient and TenantAdminClient — both
// thin wrappers around *Client — actually reach the admin listener for their
// admin/platform-only routes once AdminBaseURL is configured, using two real
// httptest servers to stand in for the two listeners.
func TestAdminBaseURL_BackupAndTenantAdminClientsRouteToAdminListener(t *testing.T) {
	var dataPlaneHit, adminHit bool

	dataPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dataPlaneHit = true
		w.WriteHeader(http.StatusNotFound) // simulates the split: route not mounted here
	}))
	defer dataPlane.Close()

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminHit = true
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin/backups":
			w.Write([]byte(`{"backups":[]}`))
		case "/platform/usage":
			w.Write([]byte(`{"usage":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer admin.Close()

	c := New(dataPlane.URL, &ClientOptions{AdminBaseURL: admin.URL})

	backup := NewBackupClient(c)
	if _, err := backup.List(context.Background()); err != nil {
		t.Fatalf("BackupClient.List: %v", err)
	}
	if !adminHit || dataPlaneHit {
		t.Fatalf("BackupClient.List: adminHit=%v dataPlaneHit=%v, want admin listener only", adminHit, dataPlaneHit)
	}

	adminHit, dataPlaneHit = false, false
	tenants := NewTenantAdminClient(c)
	if _, err := tenants.PlatformUsage(context.Background()); err != nil {
		t.Fatalf("TenantAdminClient.PlatformUsage: %v", err)
	}
	if !adminHit || dataPlaneHit {
		t.Fatalf("TenantAdminClient.PlatformUsage: adminHit=%v dataPlaneHit=%v, want admin listener only", adminHit, dataPlaneHit)
	}
}

func TestAdminListenerHint_OnBareNotFoundAgainstAdminOnlyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The shape normalize_error_body (crates/relata-cli/src/serve.rs)
		// leaves behind for a request that matched no route on this listener.
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"type":"about:blank","status":404,"title":"Not Found","detail":""}`))
	}))
	defer srv.Close()

	c := New(srv.URL, nil) // AdminBaseURL deliberately left unset
	var out map[string]any
	err := c.get(context.Background(), "/admin/backups", &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want it to still unwrap to ErrNotFound", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "RELATA_ADMIN_BIND") || !strings.Contains(msg, "/admin/backups") {
		t.Fatalf("error message missing admin-listener hint: %q", msg)
	}
}

func TestAdminListenerHint_NotAddedForRealBusiness404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"type":"about:blank","status":404,"title":"Not Found","detail":"tenant 'acme' not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	var out map[string]any
	err := c.get(context.Background(), "/platform/tenants/acme", &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "RELATA_ADMIN_BIND") {
		t.Fatalf("hint should not fire for a real business 404 with a real detail: %q", msg)
	}
	if !strings.Contains(msg, "tenant 'acme' not found") {
		t.Fatalf("expected the real server detail to be preserved: %q", msg)
	}
}

func TestAdminListenerHint_NotAddedForDataPlanePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"type":"about:blank","status":404,"title":"Not Found","detail":""}`))
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	var out map[string]any
	err := c.get(context.Background(), "/some/data/plane/route", &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "RELATA_ADMIN_BIND") {
		t.Fatalf("hint must only fire for /admin/* and /platform/* paths: %v", err)
	}
}
