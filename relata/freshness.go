package relata

import (
	"context"
	"strconv"
	"strings"
)

// FreshnessOptions configures a barrier or a gated read (relata.freshness/v1,
// #5706). Indexes may name "lexical", "secondary", "vector", "graph" or
// "extraction"; nil means every index. WaitMS is the maximum wait (server
// default 1000, max 30000); zero leaves it unset.
type FreshnessOptions struct {
	Indexes        []string
	WaitMS         int
	RequireDurable bool
	// Partial asks a gated read to return an explicitly incomplete result
	// instead of failing with 503 (headers only; ignored by Barrier).
	Partial bool
}

// FreshnessHeaders returns the headers that gate /v1/query, /v1/search, /v1/graphql or
// MCP reads on token. A read is never served as complete while an index it
// depends on is behind: it fails 503 freshness-not-ready with per-index
// progress, or, with Partial, carries freshness.status "incomplete".
func FreshnessHeaders(token string, opts *FreshnessOptions) map[string]string {
	h := map[string]string{"X-Relata-Min-Version": token}
	if opts == nil {
		return h
	}
	if opts.Indexes != nil {
		v := strings.Join(opts.Indexes, ",")
		if v == "" {
			v = "none"
		}
		h["X-Relata-Require-Index"] = v
	}
	if opts.WaitMS > 0 {
		h["X-Relata-Wait-Ms"] = strconv.Itoa(opts.WaitMS)
	}
	if opts.RequireDurable {
		h["X-Relata-Require-Durable"] = "true"
	}
	if opts.Partial {
		h["X-Relata-Freshness-Mode"] = "partial"
	}
	return h
}

// FreshnessClient wraps the freshness routes.
type FreshnessClient struct {
	c *Client
}

// NewFreshnessClient inherits the parent client's auth and tenant context.
func NewFreshnessClient(c *Client) *FreshnessClient {
	return &FreshnessClient{c: c}
}

// Status calls GET /v1/freshness/status: per-index state plus a token for the
// caller's latest accepted write.
func (f *FreshnessClient) Status(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := f.c.get(ctx, "/v1/freshness/status", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Barrier calls POST /v1/freshness/barrier and waits until token is visible in
// the named indexes. A write not visible within WaitMS returns the server's
// freshness-not-ready problem as an error.
func (f *FreshnessClient) Barrier(ctx context.Context, token string, opts *FreshnessOptions) (map[string]any, error) {
	payload := map[string]any{"token": token}
	if opts != nil {
		if opts.Indexes != nil {
			payload["indexes"] = opts.Indexes
		}
		if opts.WaitMS > 0 {
			payload["wait_ms"] = opts.WaitMS
		}
		if opts.RequireDurable {
			payload["require_durable"] = true
		}
	}
	var out map[string]any
	if err := f.c.postJSON(ctx, "/v1/freshness/barrier", payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}
