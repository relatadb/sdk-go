package relata

import "context"

// ─── Schema-BRANCH CRUD + typed edge definitions (#2497) ─────────────────────
//
// These mirror the Python RelataClient methods of the same names. They are the
// distinct schema-BRANCH surface — schema-ALTER (PATCH /v1/types/:name/schema,
// #2476) already ships on the server and is a separate concern.

// ListEdgeTypes lists registered cross-type edge definitions (ADR-007). Wraps
// GET /v1/types/edges — the edge-declaration table consumed by PATHS_BETWEEN
// traversal.
func (c *Client) ListEdgeTypes(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, "/v1/types/edges", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RegisterEdgeType declares a typed named edge for graph traversal (ADR-007).
// Wraps POST /v1/types/edges with {from_type, to_type, label}.
func (c *Client) RegisterEdgeType(ctx context.Context, fromType, toType, label string) (map[string]any, error) {
	body := map[string]any{
		"from_type": fromType,
		"to_type":   toType,
		"label":     label,
	}
	var out map[string]any
	if err := c.postJSON(ctx, "/v1/types/edges", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateSchemaBranch creates a named schema branch off fromBranch (#207). Wraps
// POST /v1/schema/branches/:name with {"from": fromBranch}. The branch carries its
// own ontology / type set; mutations on it do not affect the parent until
// merged.
func (c *Client) CreateSchemaBranch(ctx context.Context, name, fromBranch string) (map[string]any, error) {
	body := map[string]any{"from": fromBranch}
	var out map[string]any
	if err := c.postJSON(ctx, "/v1/schema/branches/"+pathSegment(name), body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteSchemaBranch deletes a named schema branch (#207). Wraps
// DELETE /v1/schema/branches/:name.
func (c *Client) DeleteSchemaBranch(ctx context.Context, name string) (map[string]any, error) {
	var out map[string]any
	if err := c.delete(ctx, "/v1/schema/branches/"+pathSegment(name), &out); err != nil {
		return nil, err
	}
	return out, nil
}
