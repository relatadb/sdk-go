package relata

import (
	"context"
)

// TokenClient is the synchronous tenant self-service token client. It lists,
// audits, rotates and revokes the caller's own bearer tokens under
// /v1/auth/tokens/self.
type TokenClient struct {
	c *Client
}

// NewTokenClient constructs a TokenClient that inherits the parent client's
// auth and tenant context. Mirrors the Python reference's TokenClient.from_client.
func NewTokenClient(c *Client) *TokenClient {
	return &TokenClient{c: c}
}

// ListSelf lists the caller's own tokens (GET /v1/auth/tokens/self).
func (t *TokenClient) ListSelf(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/v1/auth/tokens/self", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SelfAudit returns the audit trail of the caller's own tokens
// (GET /v1/auth/tokens/self/audit).
func (t *TokenClient) SelfAudit(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/v1/auth/tokens/self/audit", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RotateSelf rotates the calling token (POST /v1/auth/tokens/self/rotate) and
// returns the decoded response carrying the replacement secret.
func (t *TokenClient) RotateSelf(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.postJSON(ctx, "/v1/auth/tokens/self/rotate", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RevokeSelf revokes one of the caller's own tokens by id
// (DELETE /v1/auth/tokens/self/{id}).
func (t *TokenClient) RevokeSelf(ctx context.Context, tokenID string) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.delete(ctx, "/v1/auth/tokens/self/"+pathSegment(tokenID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// boolField pulls a boolean value from a decoded map, coercing numeric truthiness.
func boolField(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v == "true" || v == "1"
	default:
		return false
	}
}
