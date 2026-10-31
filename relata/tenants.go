package relata

import (
	"context"
)

// TenantAdminClient is the synchronous tenant self-service client. Only the
// caller's own tenant record is exposed; tenant administration is not part of
// the public SDK.
type TenantAdminClient struct {
	c *Client
}

// NewTenantAdminClient constructs a TenantAdminClient that inherits the parent
// client's auth and tenant context.
func NewTenantAdminClient(c *Client) *TenantAdminClient {
	return &TenantAdminClient{c: c}
}

// Me returns the caller's own tenant record (GET /v1/tenants/me).
func (t *TenantAdminClient) Me(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/v1/tenants/me", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
