package relata

import (
	"context"
)

// TenantAdminClient is the synchronous tenant-admin client — CRUD + quota +
// sharing agreements + platform-wide admin.
type TenantAdminClient struct {
	c *Client
}

// NewTenantAdminClient constructs a TenantAdminClient that inherits the parent
// client's auth and tenant context. Mirrors the Python reference's
// TenantAdminClient.from_client.
func NewTenantAdminClient(c *Client) *TenantAdminClient {
	return &TenantAdminClient{c: c}
}

// CreateTenantOptions configures TenantAdminClient.Create.
type CreateTenantOptions struct {
	// Tier is the tenant's service tier. Defaults to "standard".
	Tier string
	// Fields is an optional bag of extra tenant attributes (name, contact, …).
	Fields map[string]any
}

// CreateSharingOptions configures TenantAdminClient.CreateSharing.
type CreateSharingOptions struct {
	// Action is the granted action. Defaults to "read".
	Action string
	// ExpiresNS is an optional nanosecond expiry timestamp.
	ExpiresNS int64
}

// Me returns the caller's own tenant record.
func (t *TenantAdminClient) Me(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/tenants/me", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Usage returns the caller's tenant usage.
func (t *TenantAdminClient) Usage(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/tenants/usage", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Create provisions a new tenant.
func (t *TenantAdminClient) Create(ctx context.Context, tenantID string, opts *CreateTenantOptions) (map[string]any, error) {
	tier := "standard"
	payload := map[string]any{"tenant_id": tenantID, "tier": tier}
	if opts != nil {
		if opts.Tier != "" {
			payload["tier"] = opts.Tier
		}
		for k, v := range opts.Fields {
			payload[k] = v
		}
	}
	var resp map[string]any
	if err := t.c.postJSON(ctx, "/platform/tenants", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Get returns a tenant's details.
func (t *TenantAdminClient) Get(ctx context.Context, tenantID string) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/platform/tenants/"+pathSegment(tenantID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Delete deletes a tenant.
func (t *TenantAdminClient) Delete(ctx context.Context, tenantID string) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.delete(ctx, "/platform/tenants/"+pathSegment(tenantID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SetTier changes a tenant's tier.
func (t *TenantAdminClient) SetTier(ctx context.Context, tenantID, tier string) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.patchJSON(ctx, "/platform/tenants/"+pathSegment(tenantID)+"/tier", map[string]any{"tier": tier}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Suspend suspends a tenant.
func (t *TenantAdminClient) Suspend(ctx context.Context, tenantID string) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.postJSON(ctx, "/platform/tenants/"+pathSegment(tenantID)+"/suspend", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Reactivate reactivates a suspended tenant.
func (t *TenantAdminClient) Reactivate(ctx context.Context, tenantID string) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.postJSON(ctx, "/platform/tenants/"+pathSegment(tenantID)+"/reactivate", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SetQuota sets the cost-unit quota for a tenant.
func (t *TenantAdminClient) SetQuota(ctx context.Context, tenantID string, quota int) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.putJSON(ctx, "/tenants/"+pathSegment(tenantID)+"/quota", map[string]any{"quota": quota}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// TenantUsage returns a specific tenant's usage.
func (t *TenantAdminClient) TenantUsage(ctx context.Context, tenantID string) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/tenants/"+pathSegment(tenantID)+"/usage", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListSharing lists the sharing agreements for a tenant.
func (t *TenantAdminClient) ListSharing(ctx context.Context, tenantID string) ([]map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/tenants/"+pathSegment(tenantID)+"/sharing", &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "agreements"), nil
}

// CreateSharing registers a SharingAgreement so tenantID can read
// partnerTenant's rows of objectType (ADR-132 cross-org reads).
func (t *TenantAdminClient) CreateSharing(ctx context.Context, tenantID, partnerTenant, objectType string, opts *CreateSharingOptions) (map[string]any, error) {
	action := "read"
	payload := map[string]any{
		"partner_tenant": partnerTenant,
		"object_type":    objectType,
		"action":         action,
	}
	if opts != nil {
		if opts.Action != "" {
			payload["action"] = opts.Action
		}
		if opts.ExpiresNS > 0 {
			payload["expires_ns"] = opts.ExpiresNS
		}
	}
	var resp map[string]any
	if err := t.c.postJSON(ctx, "/tenants/"+pathSegment(tenantID)+"/sharing", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// PlatformUsage returns platform-wide usage (all tenants).
func (t *TenantAdminClient) PlatformUsage(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/platform/usage", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// PlatformLicense returns the platform license info (tier, max_tenants, expiry).
func (t *TenantAdminClient) PlatformLicense(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/platform/license", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
