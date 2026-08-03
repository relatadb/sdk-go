package relata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #3212 — URL path/query-injection regression tests. Each payload feeds the
// characters `../`, `?`, `#`, `&` into a path-segment or query-param helper and
// asserts the outgoing URL is percent-encoded, so the payload can neither
// traverse the path nor smuggle an extra query parameter.

// urlCaptureServer records the escaped path + raw query of the one request.
func urlCaptureServer(t *testing.T) (*httptest.Server, *string, *string) {
	t.Helper()
	path := ""
	query := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &path, &query
}

func TestURLInjection_TenantDelete_PathEncoded(t *testing.T) {
	srv, path, _ := urlCaptureServer(t)
	c := newTestClient(srv, &ClientOptions{BearerToken: "tok"})
	tc := NewTenantAdminClient(c)
	if _, err := tc.Delete(context.Background(), "../x"); err != nil {
		t.Fatal(err)
	}
	if *path != "/platform/tenants/..%2Fx" {
		t.Fatalf("path = %q, want /platform/tenants/..%%2Fx", *path)
	}
}

func TestURLInjection_GovernanceDisable_PathEncoded(t *testing.T) {
	cases := map[string]string{
		"a?b":  "/rules/a%3Fb",
		"a#b":  "/rules/a%23b",
		"a&b":  "/rules/a%26b",
		"../x": "/rules/..%2Fx",
	}
	for in, want := range cases {
		srv, path, _ := urlCaptureServer(t)
		c := newTestClient(srv, &ClientOptions{BearerToken: "tok", DefaultPurpose: "analytics"})
		g := NewGovernanceClient(c)
		if _, err := g.DisableRule(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if *path != want {
			t.Errorf("DisableRule(%q): path = %q, want %q", in, *path, want)
		}
	}
}

func TestURLInjection_A2AGetTask_PathEncoded(t *testing.T) {
	srv, path, _ := urlCaptureServer(t)
	c := newTestClient(srv, &ClientOptions{BearerToken: "tok"})
	a := NewA2AClient(c)
	if _, err := a.GetTask(context.Background(), "a#b?c&d/../e"); err != nil {
		t.Fatal(err)
	}
	if *path != "/a2a/tasks/a%23b%3Fc%26d%2F..%2Fe" {
		t.Fatalf("path = %q", *path)
	}
}

func TestURLInjection_ExportData_QueryEncoded(t *testing.T) {
	srv, path, query := urlCaptureServer(t)
	c := newTestClient(srv, &ClientOptions{BearerToken: "tok"})
	if _, err := c.ExportData(context.Background(), "Person&purpose=admin", "json"); err != nil {
		t.Fatal(err)
	}
	if *path != "/export" {
		t.Fatalf("path = %q, want /export", *path)
	}
	// The '&' in the type must be percent-encoded so it cannot smuggle a second
	// query parameter; url.Values encodes '&' -> %26 and '=' -> %3D.
	want := "format=json&purpose=export&type=Person%26purpose%3Dadmin"
	if *query != want {
		t.Fatalf("query = %q, want %q", *query, want)
	}
}

func TestURLInjection_SessionAndWebhook_PathEncoded(t *testing.T) {
	srv, path, _ := urlCaptureServer(t)
	c := newTestClient(srv, &ClientOptions{BearerToken: "tok"})
	if _, err := c.SessionDiff(context.Background(), "../s?x"); err != nil {
		t.Fatal(err)
	}
	if *path != "/session/..%2Fs%3Fx/diff" {
		t.Fatalf("path = %q", *path)
	}

	srv2, path2, _ := urlCaptureServer(t)
	c2 := newTestClient(srv2, &ClientOptions{BearerToken: "tok"})
	if _, err := c2.DeleteWebhook(context.Background(), "w#1"); err != nil {
		t.Fatal(err)
	}
	if *path2 != "/webhooks/w%231" {
		t.Fatalf("path = %q", *path2)
	}
}

func TestPathSegment_EncodesReservedChars(t *testing.T) {
	cases := map[string]string{
		"../x":     "..%2Fx",
		"a?b":      "a%3Fb",
		"a#b":      "a%23b",
		"a&b":      "a%26b",
		"plain":    "plain",
		"o'reilly": "o%27reilly",
	}
	for in, want := range cases {
		if got := pathSegment(in); got != want {
			t.Errorf("pathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}
