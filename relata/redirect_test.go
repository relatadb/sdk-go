package relata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// #3046: the #1416/#2364 no-redirect bearer-token-leak fix (already applied to
// the Rust and TypeScript SDKs) never reached the Go SDK. net/http's default
// http.Client follows up to 10 redirects and only strips Authorization on a
// cross-host hop, forwarding it unchanged on a same-host one — a server
// misconfiguration, a compromised reverse proxy, or a malicious response can
// silently exfiltrate the bearer token. This file proves both http.Client
// construction sites in client.go and S3Client.HTTP() in s3.go refuse to
// follow a redirect and never resend Authorization to the redirect target.

// newAttackerServer starts a server that must never be contacted. It counts
// hits so tests can assert it stayed at zero.
func newAttackerServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// newRedirectingServer starts a server that always responds with a 302 to
// target, and records the Authorization header of every request it receives.
func newRedirectingServer(t *testing.T, target string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var lastAuth atomic.Value
	lastAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastAuth
}

func TestClient_Query_DoesNotFollowRedirectOrResendBearerToken(t *testing.T) {
	attacker, attackerHits := newAttackerServer(t)
	redirecting, lastAuth := newRedirectingServer(t, attacker.URL+"/steal")

	c := newTestClient(redirecting, &ClientOptions{
		BearerToken:    "s3cr3t",
		DefaultPurpose: "analytics",
	})

	_, err := c.Query(context.Background(), "SELECT 1")
	if err == nil {
		t.Fatal("expected an error, the redirect must not be followed")
	}
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("expected errors.Is(err, ErrRedirectRefused), got: %v", err)
	}

	if got := lastAuth.Load().(string); got != "Bearer s3cr3t" {
		t.Fatalf("expected the original request to carry the bearer token, got %q", got)
	}
	if hits := atomic.LoadInt32(attackerHits); hits != 0 {
		t.Fatalf("attacker origin was contacted %d times — bearer token leaked", hits)
	}
}

func TestClient_CustomHTTPTimeout_DoesNotFollowRedirect(t *testing.T) {
	// Covers the opts.HTTPClient == nil branch inside the `opts != nil`
	// path of New() (client.go's second http.Client{...} construction),
	// exercised here via a non-zero opts.Timeout.
	attacker, attackerHits := newAttackerServer(t)
	redirecting, _ := newRedirectingServer(t, attacker.URL+"/steal")

	c := newTestClient(redirecting, &ClientOptions{BearerToken: "s3cr3t"})
	_, err := c.Query(context.Background(), "SELECT 1", WithPurpose("p"))
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("expected errors.Is(err, ErrRedirectRefused), got: %v", err)
	}
	if hits := atomic.LoadInt32(attackerHits); hits != 0 {
		t.Fatalf("attacker origin was contacted %d times — bearer token leaked", hits)
	}
}

func TestS3Client_HTTP_DoesNotFollowRedirectOrResendBearerToken(t *testing.T) {
	attacker, attackerHits := newAttackerServer(t)
	redirecting, lastAuth := newRedirectingServer(t, attacker.URL+"/steal")

	c := newTestClient(redirecting, &ClientOptions{BearerToken: "s3cr3t"})
	s3 := NewS3Client(c)

	_, err := s3.ListBuckets(context.Background())
	if err == nil {
		t.Fatal("expected an error, the redirect must not be followed")
	}
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("expected errors.Is(err, ErrRedirectRefused), got: %v", err)
	}

	if got := lastAuth.Load().(string); got != "Bearer s3cr3t" {
		t.Fatalf("expected the original request to carry the bearer token, got %q", got)
	}
	if hits := atomic.LoadInt32(attackerHits); hits != 0 {
		t.Fatalf("attacker origin was contacted %d times — bearer token leaked", hits)
	}
}
