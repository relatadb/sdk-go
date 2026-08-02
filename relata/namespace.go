package relata

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// namespaceNameRE is the safe-identifier gate before embedding the namespace
// name into SQL / paths. Mirrors sdks/python/relata/namespace.py:_TYPE_RE and
// the server's validate rule (^[A-Za-z_][A-Za-z0-9_]{0,127}$).
var namespaceNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateNamespaceName panics-free reports whether name is a safe namespace /
// object-type identifier. Returns an error suitable for surfacing to callers.
func validateNamespaceName(name string) error {
	if !namespaceNameRE.MatchString(name) {
		return fmt.Errorf("relata: invalid namespace/type name %q: must match ^[A-Za-z_][A-Za-z0-9_]*$", name)
	}
	return nil
}

// Namespace is a handle bound to a single object type — the T9 flagship
// retrieval surface (#1991, epic #1982). It reuses the parent Client's
// connection pool, auth, tenant, and purpose context, mirroring the Python
// Namespace (sdks/python/relata/namespace.py).
//
// Construct via [Client.Namespace]; do not instantiate directly.
//
//	docs := client.Namespace("Document")
//	_, _ = docs.Write(ctx, []map[string]any{{"id": "d1", "title": "..."}})
//	res, _ := docs.Query(ctx, relata.SearchRequest{Text: "retrieval", MatchColumn: "title"})
type Namespace struct {
	c    *Client
	Name string
}

// Namespace returns a [*Namespace] handle bound to the named object type. The
// handle is cheap to construct and carries no state beyond the parent client
// and the name; callers may create as many as they need.
//
//	docs := client.Namespace("Document")
func (c *Client) Namespace(name string) *Namespace {
	return &Namespace{c: c, Name: name}
}

// validate is an internal helper that checks the bound name before embedding it
// into SQL or paths.
func (n *Namespace) validate() error {
	return validateNamespaceName(n.Name)
}

// resolvePurpose returns the effective purpose for a namespace call: the
// per-call override when set, otherwise the parent client's DefaultPurpose.
func (n *Namespace) resolvePurpose(override string) string {
	if override != "" {
		return override
	}
	return n.c.defaultPurpose
}

// Query runs a typed ranked search over this namespace through the governed
// /search door (#1983). It delegates to [SearchClient.Query] with From pinned
// to the namespace name. PURPOSE / ACL / cell-masking / tenant isolation apply
// identically to a hand-written query — no SQL is constructed on the client
// side.
//
// The SearchRequest.From field is overridden with the namespace name; callers
// may leave it empty. See [SearchRequest] for the full field reference.
//
//	res, err := docs.Query(ctx, relata.SearchRequest{
//	    Text:        "fraud pattern",
//	    MatchColumn: "title",
//	    Filters:     []relata.SearchFilter{{Field: "status", Op: "eq", Value: "open"}},
//	})
func (n *Namespace) Query(ctx context.Context, req SearchRequest) (*QueryResult, error) {
	if err := n.validate(); err != nil {
		return nil, err
	}
	req.From = n.Name
	return NewSearchClient(n.c).Query(ctx, req)
}

// NamespaceWriteOptions configures per-call overrides for Namespace.Write.
type NamespaceWriteOptions struct {
	// Purpose overrides the parent client's default purpose.
	Purpose string
	// Schema pins field types that would otherwise be inferred (e.g.
	// "embedding" -> "bge-m3:text:1.0:1024").
	Schema map[string]string
}

// Write performs a schemaless upsert — POST /ingest/auto (T6, #1988). If the
// namespace's type does not yet exist the server creates it with field types
// inferred from the rows; the response carries type_created and inferred_schema.
//
// Each row should carry an "id" key (the primary key); re-writing with the same
// id supersedes bi-temporally.
func (n *Namespace) Write(ctx context.Context, rows []map[string]any, opts *NamespaceWriteOptions) (map[string]any, error) {
	if err := n.validate(); err != nil {
		return nil, err
	}
	body := map[string]any{
		"object_type": n.Name,
		"rows":        rows,
	}
	if opts != nil && len(opts.Schema) > 0 {
		body["schema"] = opts.Schema
	}
	purpose := n.c.defaultPurpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	if purpose != "" {
		body["purpose"] = purpose
	}
	var out map[string]any
	if err := n.c.postJSON(ctx, "/ingest/auto", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Get fetches a single row by id through a governed point-lookup SELECT, or
// returns nil when the id is absent. Purpose is resolved from the parent
// client's default or the per-call override.
func (n *Namespace) Get(ctx context.Context, objectID string, opts *NamespaceOption) (map[string]any, error) {
	if err := n.validate(); err != nil {
		return nil, err
	}
	purpose := n.c.defaultPurpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	if purpose == "" {
		return nil, ErrPurposeRequired
	}
	escaped := strings.ReplaceAll(objectID, "'", "''")
	sql := fmt.Sprintf("SELECT * FROM %s WHERE id = '%s' LIMIT 1", n.Name, escaped)
	result, err := n.c.Query(ctx, sql, WithPurpose(purpose))
	if err != nil {
		return nil, err
	}
	if len(result.Rows) == 0 {
		return nil, nil
	}
	return result.Rows[0], nil
}

// NamespaceOption is a lightweight per-call override for namespace methods that
// only take a purpose (Get, DeleteAll, BranchFrom).
type NamespaceOption struct {
	Purpose string
}

// DeleteAll governs a bulk retract — bi-temporal tombstones every row in the
// namespace via the schemaless ingest path (_deleted: true). NOT a physical
// delete — rows remain in bi-temporal history for audit/provenance but are
// excluded from subsequent reads.
//
// Returns {"deleted": N} where N is the number of tombstoned rows.
func (n *Namespace) DeleteAll(ctx context.Context, opts *NamespaceOption) (map[string]any, error) {
	if err := n.validate(); err != nil {
		return nil, err
	}
	purpose := n.c.defaultPurpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	if purpose == "" {
		return nil, ErrPurposeRequired
	}
	sql := fmt.Sprintf("SELECT id FROM %s", n.Name)
	result, err := n.c.Query(ctx, sql, WithPurpose(purpose))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, row := range result.Rows {
		if id, ok := row["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return map[string]any{"deleted": 0}, nil
	}
	tombstones := make([]map[string]any, len(ids))
	for i, id := range ids {
		tombstones[i] = map[string]any{"id": id, "_deleted": true}
	}
	body := map[string]any{
		"object_type": n.Name,
		"rows":        tombstones,
	}
	if purpose != "" {
		body["purpose"] = purpose
	}
	if err := n.c.postJSON(ctx, "/ingest/auto", body, nil); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": len(ids)}, nil
}

// BranchFrom creates this namespace as a copy-on-write branch of source. Targets
// POST /v1/namespaces/:name/branch (T10, #1992).
func (n *Namespace) BranchFrom(ctx context.Context, source string, opts *NamespaceOption) (map[string]any, error) {
	if err := n.validate(); err != nil {
		return nil, err
	}
	if err := validateNamespaceName(source); err != nil {
		return nil, err
	}
	body := map[string]any{"from": source}
	if opts != nil && opts.Purpose != "" {
		body["purpose"] = opts.Purpose
	}
	var out map[string]any
	if err := n.c.postJSON(ctx, fmt.Sprintf("/v1/namespaces/%s/branch", n.Name), body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
