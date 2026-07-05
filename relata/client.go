package relata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultTimeout        = 30 * time.Second
	defaultContentType    = "application/json"
	headerAuthorization   = "Authorization"
	headerContentType     = "Content-Type"
	headerAccept          = "Accept"
	userAgent             = "relata-go/0.1.0"
)

// Client is a thread-safe HTTP client for the Relata data engine.
// A single Client may be shared across goroutines.
type Client struct {
	baseURL        string
	bearerToken    string
	defaultPurpose string
	http           *http.Client
}

// New constructs a Client that targets baseURL. opts may be nil, in which case
// all defaults apply (30 s timeout, no authentication, no default purpose).
//
// baseURL must not have a trailing slash, e.g. "http://localhost:8080".
func New(baseURL string, opts *ClientOptions) *Client {
	baseURL = strings.TrimRight(baseURL, "/")

	c := &Client{
		baseURL: baseURL,
	}

	if opts == nil {
		c.http = &http.Client{Timeout: defaultTimeout}
		return c
	}

	c.bearerToken = opts.BearerToken
	c.defaultPurpose = opts.DefaultPurpose

	if opts.HTTPClient != nil {
		c.http = opts.HTTPClient
	} else {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = defaultTimeout
		}
		c.http = &http.Client{Timeout: timeout}
	}

	return c
}

// Query executes sql against the Relata engine and returns the result set.
//
// A purpose token must be supplied either via the DefaultPurpose field on
// ClientOptions or the WithPurpose QueryOption. If neither is present,
// ErrPurposeRequired is returned before any network call is made.
//
// Example:
//
//	result, err := client.Query(ctx,
//	    "SELECT * FROM Person AS OF '2025-01-01' LIMIT 20",
//	    relata.WithPurpose("investigation"),
//	)
func (c *Client) Query(ctx context.Context, sql string, opts ...QueryOption) (*QueryResult, error) {
	cfg := &queryConfig{
		purpose: c.defaultPurpose,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.purpose == "" {
		return nil, ErrPurposeRequired
	}

	payload := QueryRequest{
		Purpose: cfg.purpose,
		SQL:     sql,
	}

	var result QueryResult
	if err := c.post(ctx, "/query", payload, &result, cfg.timeout); err != nil {
		return nil, err
	}
	return &result, nil
}

// Health calls GET /health and returns the node health status.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.get(ctx, "/health", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Status calls GET /status and returns the node operational status.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.get(ctx, "/status", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AuditCount calls GET /audit/count and returns the audit log summary.
// If the returned ChainValid field is false, this method also returns
// ErrChainCorrupted so that callers who do not inspect the struct still
// receive an actionable error.
func (c *Client) AuditCount(ctx context.Context) (*AuditCountResponse, error) {
	var resp AuditCountResponse
	if err := c.get(ctx, "/audit/count", &resp); err != nil {
		return nil, err
	}
	if !resp.ChainValid {
		return &resp, ErrChainCorrupted
	}
	return &resp, nil
}

// ClusterNodes calls GET /cluster/nodes and returns the list of known nodes.
// On lite/server profiles the list contains only the local node.
func (c *Client) ClusterNodes(ctx context.Context) ([]ClusterNode, error) {
	var resp ClusterNodesResponse
	if err := c.get(ctx, "/cluster/nodes", &resp); err != nil {
		return nil, err
	}
	return resp.Nodes, nil
}

// ---- internal helpers -------------------------------------------------------

// get performs a GET request to path and JSON-decodes the response into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("relata: build request: %w", err)
	}
	c.setHeaders(req)
	return c.do(req, out)
}

// post performs a POST request to path with body JSON-encoded from in, and
// JSON-decodes the response into out. If timeout is non-zero, a derived context
// with that deadline replaces ctx for this call only.
func (c *Client) post(ctx context.Context, path string, in any, out any, timeout time.Duration) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("relata: marshal request: %w", err)
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("relata: build request: %w", err)
	}
	req.Header.Set(headerContentType, defaultContentType)
	c.setHeaders(req)
	return c.do(req, out)
}

// setHeaders applies the common headers to every outbound request.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set(headerAccept, defaultContentType)
	req.Header.Set("User-Agent", userAgent)
	if c.bearerToken != "" {
		req.Header.Set(headerAuthorization, "Bearer "+c.bearerToken)
	}
}

// do executes req, reads the body, checks the status code, and decodes the
// JSON response into out.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("relata: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("relata: read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorFromStatus(resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("relata: decode response: %w", err)
	}
	return nil
}
