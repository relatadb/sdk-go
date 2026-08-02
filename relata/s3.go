package relata

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// S3Client is a native net/http wrapper for RelataDB's built-in S3-compatible
// protocol door. The Go SDK does NOT depend on boto3 or any external S3
// library — callers either use the returned *http.Client from HTTP() to issue
// arbitrary S3 REST verbs, or use the high-level ListBuckets/CreateBucket/
// PutObject/GetObject/DeleteObject helpers below (mirroring the TypeScript
// reference's listBuckets/createBucket/putObject/getObject/deleteObject,
// s3.ts:178-214) for the common cases.
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
		// #3046: never follow a redirect — parity with Client's http.Client
		// (client.go's refuseRedirects) so a 3xx response can't be used to
		// exfiltrate the bearer token this transport injects on every
		// request.
		CheckRedirect: refuseRedirects,
	}
}

// BaseURL returns the S3 door endpoint URL (no trailing slash).
func (s *S3Client) BaseURL() string { return s.EndpointURL }

// S3HttpResponse is the decoded response from the high-level S3 helpers below
// (ListBuckets/CreateBucket/PutObject/GetObject/DeleteObject) and from
// doRequest. Mirrors the TypeScript reference's S3HttpResponse (s3.ts).
type S3HttpResponse struct {
	// Status is the HTTP status code returned by the door.
	Status int
	// Headers holds every response header, lower-cased keys.
	Headers map[string]string
	// Body is the raw response body.
	Body []byte
}

// PutObjectOptions configures PutObject's optional Content-Type header.
type PutObjectOptions struct {
	// ContentType sets the Content-Type header, when non-empty.
	ContentType string
}

// doRequest issues a raw S3 REST verb against the door via HTTP(nil) and
// decodes the response into an S3HttpResponse. It does not translate non-2xx
// statuses into errors — like the TypeScript reference, callers inspect
// Status/Body themselves (S3 semantics: e.g. GetObject on a missing key is a
// valid 404 response, not a Go error). Only transport-level failures (DNS,
// connection refused, context cancellation, …) are returned as errors.
func (s *S3Client) doRequest(ctx context.Context, method, path string, body []byte, headers map[string]string) (*S3HttpResponse, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.EndpointURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("relata: build S3 request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.HTTP(nil).Do(req)
	if err != nil {
		return nil, fmt.Errorf("relata: S3 request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("relata: read S3 response: %w", err)
	}
	respHeaders := make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		if len(v) > 0 {
			respHeaders[strings.ToLower(k)] = v[0]
		}
	}
	return &S3HttpResponse{Status: resp.StatusCode, Headers: respHeaders, Body: data}, nil
}

// ListBuckets issues GET / — list buckets. The body is the server's raw
// ListBucketsResult XML.
func (s *S3Client) ListBuckets(ctx context.Context) (*S3HttpResponse, error) {
	return s.doRequest(ctx, http.MethodGet, "/", nil, nil)
}

// CreateBucket issues PUT /<bucket> — create a bucket.
func (s *S3Client) CreateBucket(ctx context.Context, bucket string) (*S3HttpResponse, error) {
	return s.doRequest(ctx, http.MethodPut, "/"+bucket, nil, nil)
}

// PutObject issues PUT /<bucket>/<key> — upload an object. The response
// ETag (SHA-256 of the body) is in Headers["etag"].
func (s *S3Client) PutObject(ctx context.Context, bucket, key string, body []byte, opts *PutObjectOptions) (*S3HttpResponse, error) {
	var headers map[string]string
	if opts != nil && opts.ContentType != "" {
		headers = map[string]string{"Content-Type": opts.ContentType}
	}
	return s.doRequest(ctx, http.MethodPut, "/"+bucket+"/"+key, body, headers)
}

// GetObject issues GET /<bucket>/<key> — download an object. The body is on
// the returned S3HttpResponse.Body.
func (s *S3Client) GetObject(ctx context.Context, bucket, key string) (*S3HttpResponse, error) {
	return s.doRequest(ctx, http.MethodGet, "/"+bucket+"/"+key, nil, nil)
}

// DeleteObject issues DELETE /<bucket>/<key> — delete an object.
func (s *S3Client) DeleteObject(ctx context.Context, bucket, key string) (*S3HttpResponse, error) {
	return s.doRequest(ctx, http.MethodDelete, "/"+bucket+"/"+key, nil, nil)
}

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
