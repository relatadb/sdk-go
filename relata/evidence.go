package relata

import (
	"context"
	"net/url"
	"strconv"
)

// EvidenceHeaders returns the headers that record relata.evidence/v1 for a
// /v1/query, /v1/search, /v1/graphql or MCP read (#5707). retentionDays is 1 to 365;
// zero leaves the server default (30).
func EvidenceHeaders(retentionDays int) map[string]string {
	h := map[string]string{"X-Relata-Record-Evidence": "true"}
	if retentionDays > 0 {
		h["X-Relata-Evidence-Retention-Days"] = strconv.Itoa(retentionDays)
	}
	return h
}

// EvidenceClient wraps the evidence routes.
type EvidenceClient struct {
	c *Client
}

// NewEvidenceClient inherits the parent client's auth and tenant context.
func NewEvidenceClient(c *Client) *EvidenceClient {
	return &EvidenceClient{c: c}
}

// List calls GET /v1/evidence: the caller's unexpired records, newest first.
// limit <= 0 uses the server default.
func (e *EvidenceClient) List(ctx context.Context, limit int) (map[string]any, error) {
	path := "/v1/evidence"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out map[string]any
	if err := e.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Get calls GET /v1/evidence/{id}: 404 when unknown, 410 once expired.
func (e *EvidenceClient) Get(ctx context.Context, id string) (map[string]any, error) {
	var out map[string]any
	if err := e.c.get(ctx, "/v1/evidence/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Replay calls POST /v1/evidence/{id}/replay: every citation is re-read under the
// caller's current authorization. Ranking is not recomputed.
func (e *EvidenceClient) Replay(ctx context.Context, id string, includeContent bool) (map[string]any, error) {
	payload := map[string]any{}
	if includeContent {
		payload["include_content"] = true
	}
	var out map[string]any
	if err := e.c.postJSON(ctx, "/v1/evidence/"+url.PathEscape(id)+"/replay", payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}
