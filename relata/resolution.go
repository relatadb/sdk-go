package relata

import (
	"context"
	"net/url"
	"strconv"
)

// ResolutionClient wraps /v1/resolution (#5712): entity-resolution rule sets,
// candidate matches, and reviewed, reversible merges. A merge never rewrites
// the source records; Lookup and Entity answer the canonical id.
type ResolutionClient struct {
	c *Client
}

// NewResolutionClient inherits the parent client's auth and tenant.
func NewResolutionClient(c *Client) *ResolutionClient {
	return &ResolutionClient{c: c}
}

func rulesetPath(name string, rest ...string) string {
	p := "/v1/resolution/rulesets/" + url.PathEscape(name)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

func (r *ResolutionClient) get(ctx context.Context, path string) (map[string]any, error) {
	var out map[string]any
	if err := r.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ResolutionClient) post(ctx context.Context, path string, in any) (map[string]any, error) {
	var out map[string]any
	if err := r.c.postJSON(ctx, path, in, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListRulesets calls GET /v1/resolution/rulesets.
func (r *ResolutionClient) ListRulesets(ctx context.Context) (map[string]any, error) {
	return r.get(ctx, "/v1/resolution/rulesets")
}

// GetRuleset calls GET /v1/resolution/rulesets/{name}; version 0 is the latest.
func (r *ResolutionClient) GetRuleset(ctx context.Context, name string, version int) (map[string]any, error) {
	p := rulesetPath(name)
	if version > 0 {
		p += "?version=" + strconv.Itoa(version)
	}
	return r.get(ctx, p)
}

// PublishRuleset calls PUT /v1/resolution/rulesets/{name} under the
// expectedVersion precondition (0: no version yet). A mismatch is 412
// resolution-version-conflict.
func (r *ResolutionClient) PublishRuleset(ctx context.Context, name string, definition map[string]any, expectedVersion int) (map[string]any, error) {
	var out map[string]any
	body := map[string]any{"expected_version": expectedVersion, "definition": definition}
	if err := r.c.putJSON(ctx, rulesetPath(name), body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Run calls POST .../run: generate or refresh candidates (repeat while
// "complete" is false). proposals carries optional model scores.
func (r *ResolutionClient) Run(ctx context.Context, name string, proposals []map[string]any) (map[string]any, error) {
	body := map[string]any{}
	if proposals != nil {
		body["proposals"] = proposals
	}
	return r.post(ctx, rulesetPath(name, "run"), body)
}

// Evaluate calls POST .../evaluate: precision and recall on labeled pairs
// ({a, b, same}), nothing persisted.
func (r *ResolutionClient) Evaluate(ctx context.Context, name string, labeled []map[string]any, proposals []map[string]any) (map[string]any, error) {
	body := map[string]any{"labeled": labeled}
	if proposals != nil {
		body["proposals"] = proposals
	}
	return r.post(ctx, rulesetPath(name, "evaluate"), body)
}

// ListCandidates calls GET /v1/resolution/candidates; empty filters and a zero
// limit are omitted.
func (r *ResolutionClient) ListCandidates(ctx context.Context, ruleset, state string, limit int) (map[string]any, error) {
	q := url.Values{}
	if ruleset != "" {
		q.Set("ruleset", ruleset)
	}
	if state != "" {
		q.Set("state", state)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	p := "/v1/resolution/candidates"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	return r.get(ctx, p)
}

// GetCandidate calls GET /v1/resolution/candidates/{id}: evidence, history and
// live normalized values.
func (r *ResolutionClient) GetCandidate(ctx context.Context, id string) (map[string]any, error) {
	return r.get(ctx, "/v1/resolution/candidates/"+url.PathEscape(id))
}

// Decide calls POST .../decision with "approve", "reject" or "unmerge".
// Repeating a decision is a no-op (idempotent).
func (r *ResolutionClient) Decide(ctx context.Context, id, decision, reason string) (map[string]any, error) {
	return r.post(ctx, "/v1/resolution/candidates/"+url.PathEscape(id)+"/decision", map[string]any{"decision": decision, "reason": reason})
}

// Approve merges a candidate.
func (r *ResolutionClient) Approve(ctx context.Context, id, reason string) (map[string]any, error) {
	return r.Decide(ctx, id, "approve", reason)
}

// Reject rejects a candidate; the pair is not proposed again.
func (r *ResolutionClient) Reject(ctx context.Context, id, reason string) (map[string]any, error) {
	return r.Decide(ctx, id, "reject", reason)
}

// Unmerge undoes an approved merge.
func (r *ResolutionClient) Unmerge(ctx context.Context, id, reason string) (map[string]any, error) {
	return r.Decide(ctx, id, "unmerge", reason)
}

// Entity calls GET /v1/resolution/entities/{canonical}; merge aliases resolve to
// the survivor and a split id lists its successors.
func (r *ResolutionClient) Entity(ctx context.Context, canonical string) (map[string]any, error) {
	return r.get(ctx, "/v1/resolution/entities/"+url.PathEscape(canonical))
}

// Lookup calls GET /v1/resolution/lookup: the canonical id of a source record.
func (r *ResolutionClient) Lookup(ctx context.Context, typeName, id string) (map[string]any, error) {
	q := url.Values{}
	q.Set("type", typeName)
	q.Set("id", id)
	return r.get(ctx, "/v1/resolution/lookup?"+q.Encode())
}
