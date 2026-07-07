package relata

import (
	"context"
	"fmt"
)

// TokenStats holds dedup-token-set statistics.
type TokenStats struct {
	// Count is the current number of tracked tokens.
	Count int64
	// OldestNS is the nanosecond timestamp of the oldest token, or 0 when empty.
	OldestNS int64
	// EvictionPolicy is the server's eviction-policy name.
	EvictionPolicy string
}

// TokenClient is the synchronous dedup / uniqueness token client. It provides
// atomic test-and-set (Remember), presence check (Check), explicit revoke
// (Revoke — admin only), and Stats. This is the replay-defence primitive.
type TokenClient struct {
	c *Client
}

// NewTokenClient constructs a TokenClient that inherits the parent client's
// auth and tenant context. Mirrors the Python reference's TokenClient.from_client.
func NewTokenClient(c *Client) *TokenClient {
	return &TokenClient{c: c}
}

// TokenRememberOptions configures TokenClient.Remember.
type TokenRememberOptions struct {
	// TTLSecs sets an optional per-token expiry.
	TTLSecs int64
}

// Remember performs an atomic test-and-set. Returns true if newly inserted,
// false if already present. A backend that buffers the write asynchronously or
// loses the token on restart fails the §7 contract.
func (t *TokenClient) Remember(ctx context.Context, tokenID string, opts *TokenRememberOptions) (bool, error) {
	payload := map[string]any{"id": tokenID}
	if opts != nil && opts.TTLSecs > 0 {
		payload["ttl_secs"] = opts.TTLSecs
	}
	var resp map[string]any
	if err := t.c.postJSON(ctx, "/tokens", payload, &resp); err != nil {
		return false, err
	}
	return boolField(resp, "newly_inserted"), nil
}

// Check is a non-mutating presence check. Returns the decoded server response
// (present, inserted_at_ns, ttl_secs).
func (t *TokenClient) Check(ctx context.Context, tokenID string) (map[string]any, error) {
	var resp map[string]any
	if err := t.c.get(ctx, fmt.Sprintf("/tokens/%s", tokenID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Revoke explicitly removes a token (admin only). Returns true if a token was
// actually removed.
func (t *TokenClient) Revoke(ctx context.Context, tokenID string) (bool, error) {
	var resp map[string]any
	if err := t.c.delete(ctx, fmt.Sprintf("/tokens/%s", tokenID), &resp); err != nil {
		return false, err
	}
	return boolField(resp, "revoked"), nil
}

// Stats returns dedup-set statistics.
func (t *TokenClient) Stats(ctx context.Context) (TokenStats, error) {
	var resp map[string]any
	if err := t.c.get(ctx, "/tokens/stats", &resp); err != nil {
		return TokenStats{}, err
	}
	stats := TokenStats{
		Count:          int64Field(resp, "count"),
		EvictionPolicy: stringField(resp, "eviction_policy"),
	}
	if v, ok := resp["oldest_ns"]; ok && v != nil {
		stats.OldestNS = int64Field(resp, "oldest_ns")
	}
	return stats, nil
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
