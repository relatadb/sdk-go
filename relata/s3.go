package relata

import (
	"net/http"
	"strings"
	"time"
)

// S3Client is a native net/http wrapper for RelataDB's built-in S3-compatible
// protocol door. The Go SDK does NOT depend on boto3 or any external S3
// library — callers use the returned *http.Client to issue standard S3 REST
// verbs (GET/PUT/DELETE on /<bucket>/<key>) against the door.
//
// The bearer token is sent as "Authorization: Bearer <token>"; the tenant, if
// set, is sent as "X-Relata-Tenant-Id". Path-style addressing is used.
type S3Client struct {
	// EndpointURL is the base URL of the S3 door (no trailing slash).
	EndpointURL string
	// BearerToken is sent as Authorization: Bearer.
	BearerToken string
	// Tenant is sent as X-Relata-Tenant-Id when non-empty.
	Tenant string
	// Region is the (cosmetic) AWS region; the door does not validate it.
	Region string
}

// NewS3Client constructs an S3Client that inherits the parent client's base
// URL, bearer token, and tenant. Mirrors the Python reference's
// S3Client.from_client. The region defaults to "us-east-1".
func NewS3Client(c *Client) *S3Client {
	return &S3Client{
		EndpointURL: strings.TrimRight(c.baseURL, "/"),
		BearerToken: c.bearerToken,
		Tenant:      c.tenant,
		Region:      "us-east-1",
	}
}

// HTTPOptions configures the *http.Client returned by HTTP.
type HTTPOptions struct {
	// Timeout is the per-request timeout. Defaults to 30s.
	Timeout time.Duration
}

// HTTP returns a configured *http.Client pointed at the S3 door. The client's
// base URL is EndpointURL; callers issue standard S3 REST verbs against
// /<bucket>/<key>. Auth + tenant headers are pre-set via a wrapping RoundTripper.
//
// This is the zero-dependency native equivalent of the Python reference's
// S3Client.httpx() factory.
func (s *S3Client) HTTP(opts *HTTPOptions) *http.Client {
	timeout := 30 * time.Second
	if opts != nil && opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	base := http.DefaultTransport
	if opts != nil {
		// Intentionally minimal — callers wanting a custom transport wrap it
		// themselves before calling HTTP().
	}
	return &http.Client{
		Transport: &s3BearerTransport{
			base: base,
			tok:  s.BearerToken,
			tnt:  s.Tenant,
		},
		Timeout: timeout,
	}
}

// BaseURL returns the S3 door endpoint URL (no trailing slash).
func (s *S3Client) BaseURL() string { return s.EndpointURL }

// s3BearerTransport wraps an http.RoundTripper to inject the bearer + tenant
// headers on every S3 request, so callers can use a plain *http.Client without
// re-stating auth per call.
type s3BearerTransport struct {
	base http.RoundTripper
	tok  string
	tnt  string
}

// RoundTrip implements http.RoundTripper.
func (t *s3BearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid mutating the caller's input.
	clone := req.Clone(req.Context())
	if t.tok != "" {
		clone.Header.Set("Authorization", "Bearer "+t.tok)
	}
	if t.tnt != "" {
		clone.Header.Set("X-Relata-Tenant-Id", t.tnt)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
