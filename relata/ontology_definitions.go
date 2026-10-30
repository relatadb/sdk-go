package relata

import (
	"context"
	"net/url"
	"strconv"
)

// PublishOptions configures OntologyDefinitionsClient.Publish (#5711).
type PublishOptions struct {
	// ExpectedVersion is the published version the change is based on (0:
	// none yet). A mismatch is 412 ontology-version-conflict.
	ExpectedVersion int
	AcceptMigration bool
	AcceptBreaking  bool
	// FromVersion republishes an earlier version's content as the next one
	// when non-zero (forward-only rollback).
	FromVersion int
}

// OntologyDefinitionsClient wraps /ontology/definitions: versioned ontology
// definitions with compare/impact, publication under a version precondition
// and resumable data migration.
type OntologyDefinitionsClient struct {
	c *Client
}

// NewOntologyDefinitionsClient inherits the parent client's auth and tenant.
func NewOntologyDefinitionsClient(c *Client) *OntologyDefinitionsClient {
	return &OntologyDefinitionsClient{c: c}
}

func ontoPath(name string, rest ...string) string {
	p := "/ontology/definitions/" + url.PathEscape(name)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

func (o *OntologyDefinitionsClient) get(ctx context.Context, path string) (map[string]any, error) {
	var out map[string]any
	if err := o.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (o *OntologyDefinitionsClient) post(ctx context.Context, path string, in any) (map[string]any, error) {
	var out map[string]any
	if err := o.c.postJSON(ctx, path, in, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// List calls GET /ontology/definitions.
func (o *OntologyDefinitionsClient) List(ctx context.Context) (map[string]any, error) {
	return o.get(ctx, "/ontology/definitions")
}

// Create calls POST /ontology/definitions: a new set with an initial draft.
func (o *OntologyDefinitionsClient) Create(ctx context.Context, name string, definition map[string]any) (map[string]any, error) {
	return o.post(ctx, "/ontology/definitions", map[string]any{"name": name, "definition": definition})
}

// Get calls GET /ontology/definitions/{name}: head and current draft.
func (o *OntologyDefinitionsClient) Get(ctx context.Context, name string) (map[string]any, error) {
	return o.get(ctx, ontoPath(name))
}

// SaveDraft calls PUT /ontology/definitions/{name}/draft.
func (o *OntologyDefinitionsClient) SaveDraft(ctx context.Context, name string, definition map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := o.c.putJSON(ctx, ontoPath(name, "draft"), map[string]any{"definition": definition}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Validate calls POST .../validate: the draft, or definition when non-nil.
func (o *OntologyDefinitionsClient) Validate(ctx context.Context, name string, definition map[string]any) (map[string]any, error) {
	body := map[string]any{}
	if definition != nil {
		body["definition"] = definition
	}
	return o.post(ctx, ontoPath(name, "validate"), body)
}

// Compare calls GET .../compare. from and to are a version number or "draft";
// empty uses the server default (published version -> draft).
func (o *OntologyDefinitionsClient) Compare(ctx context.Context, name, from, to string) (map[string]any, error) {
	q := url.Values{}
	if from != "" {
		q.Set("from", from)
	}
	if to != "" {
		q.Set("to", to)
	}
	path := ontoPath(name, "compare")
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return o.get(ctx, path)
}

// Publish calls POST .../publish under the ExpectedVersion precondition.
func (o *OntologyDefinitionsClient) Publish(ctx context.Context, name string, opts PublishOptions) (map[string]any, error) {
	body := map[string]any{"expected_version": opts.ExpectedVersion}
	if opts.AcceptMigration {
		body["accept_migration"] = true
	}
	if opts.AcceptBreaking {
		body["accept_breaking"] = true
	}
	if opts.FromVersion > 0 {
		body["from_version"] = opts.FromVersion
	}
	return o.post(ctx, ontoPath(name, "publish"), body)
}

// Version calls GET .../versions/{version}: an immutable published version.
func (o *OntologyDefinitionsClient) Version(ctx context.Context, name string, version int) (map[string]any, error) {
	return o.get(ctx, ontoPath(name, "versions", strconv.Itoa(version)))
}

// Status calls GET .../status: row counts by definition version, stale rows.
func (o *OntologyDefinitionsClient) Status(ctx context.Context, name string) (map[string]any, error) {
	return o.get(ctx, ontoPath(name, "status"))
}

// Migration calls GET .../migrations/{version}: progress, failures, cursor.
func (o *OntologyDefinitionsClient) Migration(ctx context.Context, name string, version int) (map[string]any, error) {
	return o.get(ctx, ontoPath(name, "migrations", strconv.Itoa(version)))
}

// RunMigration calls POST .../migrations/{version}/run: it rewrites up to
// maxRows rows (0: server default) and persists the cursor; call again to
// resume. retryFailed starts a fresh pass over a finished job.
func (o *OntologyDefinitionsClient) RunMigration(ctx context.Context, name string, version, maxRows int, retryFailed bool) (map[string]any, error) {
	body := map[string]any{}
	if maxRows > 0 {
		body["max_rows"] = maxRows
	}
	if retryFailed {
		body["retry_failed"] = true
	}
	return o.post(ctx, ontoPath(name, "migrations", strconv.Itoa(version), "run"), body)
}
