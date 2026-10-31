package relata

import (
	"context"
	"fmt"
)

// AuditClient is the synchronous audit + provenance client. It wraps the
// audit count and filtered audit log projections.
type AuditClient struct {
	c *Client
}

// NewAuditClient constructs an AuditClient that inherits the parent client's
// auth and tenant context. Mirrors the Python reference's AuditClient.from_client.
func NewAuditClient(c *Client) *AuditClient {
	return &AuditClient{c: c}
}

// Count returns the audit-entry count + chain integrity flag.
func (a *AuditClient) Count(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := a.c.get(ctx, "/v1/audit/count", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// AuditLogOptions configures AuditClient.Log.
type AuditLogOptions struct {
	// Limit caps the response (server default 100, max 1000).
	Limit int
	// From is an inclusive timestamp lower bound.
	From string
	// To is an inclusive timestamp upper bound.
	To string
	// Table is a substring of the derived resource.
	Table string
	// TenantID applies only to an unscoped admin caller; an attributed
	// caller's own tenant always wins.
	TenantID string
}

// Log returns filtered mutation-style audit projections (GET /v1/audit/log) as
// {"entries": [...], "chain_valid": bool}.
func (a *AuditClient) Log(ctx context.Context, opts *AuditLogOptions) (map[string]any, error) {
	params := map[string]string{"limit": "100"}
	if opts != nil {
		if opts.Limit > 0 {
			params["limit"] = fmt.Sprintf("%d", opts.Limit)
		}
		if opts.From != "" {
			params["from"] = opts.From
		}
		if opts.To != "" {
			params["to"] = opts.To
		}
		if opts.Table != "" {
			params["table"] = opts.Table
		}
		if opts.TenantID != "" {
			params["tenant_id"] = opts.TenantID
		}
	}
	var resp map[string]any
	if err := a.c.get(ctx, encodeGetURL("/v1/audit/log", params), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
