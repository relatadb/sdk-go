package relata

import (
	"context"
	"fmt"
)

// AuditClient is the synchronous audit + provenance client. It wraps audit
// entry listing (filtered/paginated) and query correlation.
type AuditClient struct {
	c *Client
}

// NewAuditClient constructs an AuditClient that inherits the parent client's
// auth and tenant context. Mirrors the Python reference's AuditClient.from_client.
func NewAuditClient(c *Client) *AuditClient {
	return &AuditClient{c: c}
}

// AuditEntriesOptions configures the AuditClient.Entries query.
type AuditEntriesOptions struct {
	// Principal filters to one principal id.
	Principal string
	// Purpose filters to one purpose token.
	Purpose string
	// Decision filters to one ACL decision ("allow"/"deny").
	Decision string
	// RequestID filters to one X-Request-ID.
	RequestID string
	// SinceNS filters to entries at/after the nanosecond timestamp.
	SinceNS int64
	// UntilNS filters to entries at/before the nanosecond timestamp.
	UntilNS int64
	// Limit caps the response. Defaults to 100.
	Limit int
	// Cursor is the opaque pagination cursor from a prior response.
	Cursor string
}

// Count returns the audit-entry count + chain integrity flag.
func (a *AuditClient) Count(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := a.c.get(ctx, "/audit/count", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Entries is a paginated audit-entry listing with filters. Returns
// {"entries": [...], "next_cursor": str, "chain_valid": bool}.
func (a *AuditClient) Entries(ctx context.Context, opts *AuditEntriesOptions) (map[string]any, error) {
	limit := 100
	params := map[string]string{"limit": fmt.Sprintf("%d", limit)}
	if opts != nil {
		if opts.Limit > 0 {
			params["limit"] = fmt.Sprintf("%d", opts.Limit)
		}
		if opts.Principal != "" {
			params["principal"] = opts.Principal
		}
		if opts.Purpose != "" {
			params["purpose"] = opts.Purpose
		}
		if opts.Decision != "" {
			params["decision"] = opts.Decision
		}
		if opts.RequestID != "" {
			params["request_id"] = opts.RequestID
		}
		if opts.SinceNS > 0 {
			params["since_ns"] = fmt.Sprintf("%d", opts.SinceNS)
		}
		if opts.UntilNS > 0 {
			params["until_ns"] = fmt.Sprintf("%d", opts.UntilNS)
		}
		if opts.Cursor != "" {
			params["cursor"] = opts.Cursor
		}
	}
	var resp map[string]any
	if err := a.c.get(ctx, encodeGetURL("/audit/entries", params), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// FindByRequestID looks up a single audit entry by its request_id. Returns nil
// when no entry matches (the server returns an empty page).
func (a *AuditClient) FindByRequestID(ctx context.Context, requestID string) (map[string]any, error) {
	page, err := a.Entries(ctx, &AuditEntriesOptions{RequestID: requestID, Limit: 1})
	if err != nil {
		return nil, err
	}
	entries, ok := page["entries"].([]any)
	if !ok || len(entries) == 0 {
		return nil, nil
	}
	if m, ok := entries[0].(map[string]any); ok {
		return m, nil
	}
	return nil, nil
}
