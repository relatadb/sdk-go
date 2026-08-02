package relata

import "context"

// ─── Schema-BRANCH CRUD + typed edge definitions (#2497) ─────────────────────
//
// These mirror the Rust flat client (crates/relata-sdk-rust/src/http.rs:2535-
// 2570) and the Python RelataClient methods of the same names. They are the
// distinct schema-BRANCH surface — schema-ALTER (PATCH /types/:name/schema,
// #2476) already ships on the server and is a separate concern.

// ListEdgeTypes lists registered cross-type edge definitions (ADR-007). Wraps
// GET /types/edges — the edge-declaration table consumed by PATHS_BETWEEN
// traversal.
func (c *Client) ListEdgeTypes(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, "/types/edges", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RegisterEdgeType declares a typed named edge for graph traversal (ADR-007).
// Wraps POST /types/edges with {from_type, to_type, label}.
func (c *Client) RegisterEdgeType(ctx context.Context, fromType, toType, label string) (map[string]any, error) {
	body := map[string]any{
		"from_type": fromType,
		"to_type":   toType,
		"label":     label,
	}
	var out map[string]any
	if err := c.postJSON(ctx, "/types/edges", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateSchemaBranch creates a named schema branch off fromBranch (#207). Wraps
// POST /schema/branches/:name with {"from": fromBranch}. The branch carries its
// own ontology / type set; mutations on it do not affect the parent until
// merged.
func (c *Client) CreateSchemaBranch(ctx context.Context, name, fromBranch string) (map[string]any, error) {
	body := map[string]any{"from": fromBranch}
	var out map[string]any
	if err := c.postJSON(ctx, "/schema/branches/"+name, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteSchemaBranch deletes a named schema branch (#207). Wraps
// DELETE /schema/branches/:name.
func (c *Client) DeleteSchemaBranch(ctx context.Context, name string) (map[string]any, error) {
	var out map[string]any
	if err := c.delete(ctx, "/schema/branches/"+name, &out); err != nil {
		return nil, err
	}
	return out, nil
}
