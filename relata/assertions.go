package relata

import (
	"context"
	"net/url"
	"strconv"
)

// AssertionsClient wraps /v1/assertions (#5713): claims with valid time,
// recorded time and reviewed corrections. Resolve answers what is true for a
// predicate at a valid time as known at a recorded time (integer
// nanoseconds), with the verdict and reason for every assertion considered.
type AssertionsClient struct {
	c *Client
}

// NewAssertionsClient inherits the parent client's auth and tenant.
func NewAssertionsClient(c *Client) *AssertionsClient {
	return &AssertionsClient{c: c}
}

func (a *AssertionsClient) get(ctx context.Context, path string) (map[string]any, error) {
	var out map[string]any
	if err := a.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *AssertionsClient) post(ctx context.Context, path string, in any) (map[string]any, error) {
	var out map[string]any
	if err := a.c.postJSON(ctx, path, in, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Record calls POST /v1/assertions: record a claim; idempotent on claim, source
// version and derivation.
func (a *AssertionsClient) Record(ctx context.Context, assertion map[string]any) (map[string]any, error) {
	return a.post(ctx, "/v1/assertions", assertion)
}

// List calls GET /v1/assertions; empty filters and a zero limit are omitted.
func (a *AssertionsClient) List(ctx context.Context, subjectType, subjectID, predicate, state string, limit int) (map[string]any, error) {
	q := url.Values{}
	for k, v := range map[string]string{"subject_type": subjectType, "subject_id": subjectID, "predicate": predicate, "state": state} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	p := "/v1/assertions"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	return a.get(ctx, p)
}

// Get calls GET /v1/assertions/{id}.
func (a *AssertionsClient) Get(ctx context.Context, id string) (map[string]any, error) {
	return a.get(ctx, "/v1/assertions/"+url.PathEscape(id))
}

// Decide calls POST .../decision with "confirm" or "reject"; idempotent.
func (a *AssertionsClient) Decide(ctx context.Context, id, decision, reason string) (map[string]any, error) {
	return a.post(ctx, "/v1/assertions/"+url.PathEscape(id)+"/decision", map[string]any{"decision": decision, "reason": reason})
}

// Confirm confirms an assertion.
func (a *AssertionsClient) Confirm(ctx context.Context, id, reason string) (map[string]any, error) {
	return a.Decide(ctx, id, "confirm", reason)
}

// Reject rejects an assertion; it never resolves while rejected.
func (a *AssertionsClient) Reject(ctx context.Context, id, reason string) (map[string]any, error) {
	return a.Decide(ctx, id, "reject", reason)
}

// Revoke calls POST .../revoke: replace the assertion with a content-free
// tombstone. A legal hold or WORM retention on the subject is 409
// assertion-retention-blocked.
func (a *AssertionsClient) Revoke(ctx context.Context, id, reason string) (map[string]any, error) {
	return a.post(ctx, "/v1/assertions/"+url.PathEscape(id)+"/revoke", map[string]any{"reason": reason})
}

// Relate calls POST /v1/assertions/relations (#5721): link two assertions with
// kind "supports", "contradicts", "derived_from" (or a reviewer's
// "same_group_as" / "distinct_from"), a rationale and an
// optional locator ({"evidence_id": ..., "pointer": ...}; nil for none).
func (a *AssertionsClient) Relate(ctx context.Context, from, kind, to, rationale string, locator map[string]any) (map[string]any, error) {
	body := map[string]any{"from": from, "to": to, "kind": kind, "rationale": rationale}
	if locator != nil {
		body["locator"] = locator
	}
	return a.post(ctx, "/v1/assertions/relations", body)
}

// RelateSimilar calls POST /v1/assertions/relations with kind "similar_to": the
// score in [0, 1] and method come from the caller's ETL step.
func (a *AssertionsClient) RelateSimilar(ctx context.Context, from, to, rationale string, score float64, method string, locator map[string]any) (map[string]any, error) {
	body := map[string]any{"from": from, "to": to, "kind": "similar_to", "rationale": rationale,
		"similarity": map[string]any{"score": score, "method": method}}
	if locator != nil {
		body["locator"] = locator
	}
	return a.post(ctx, "/v1/assertions/relations", body)
}

// Unrelate calls POST /v1/assertions/relations/{id}/revoke: withdraw a relation.
func (a *AssertionsClient) Unrelate(ctx context.Context, relationID, reason string) (map[string]any, error) {
	return a.post(ctx, "/v1/assertions/relations/"+url.PathEscape(relationID)+"/revoke", map[string]any{"reason": reason})
}

// Assess calls POST .../assessment: an analyst judgment ("corroborated",
// "disputed" or "inconclusive") kept apart from source reliability and model
// confidence.
func (a *AssertionsClient) Assess(ctx context.Context, id, judgment, reason string) (map[string]any, error) {
	return a.post(ctx, "/v1/assertions/"+url.PathEscape(id)+"/assessment", map[string]any{"judgment": judgment, "reason": reason})
}

// EvidenceGraphOptions are the optional parameters of EvidenceGraph.
type EvidenceGraphOptions struct {
	// Depth is the number of hops (1 to 3); 0 is the server default.
	Depth int
	// Limit is the most assertions returned (1 to 200); 0 is the server default.
	Limit int
	// Policy names a published policy supplying source_reliability.
	Policy string
	// PolicyVersion selects a policy version; 0 is the latest.
	PolicyVersion int
	// MinSimilarity is the score at which similar_to relations join a group;
	// nil is the server default (0.8).
	MinSimilarity *float64
}

// EvidenceGraph calls GET .../evidence-graph: the bounded, authorized
// neighbourhood of an assertion plus its corroboration summary.
func (a *AssertionsClient) EvidenceGraph(ctx context.Context, id string, opts EvidenceGraphOptions) (map[string]any, error) {
	q := url.Values{}
	if opts.Depth > 0 {
		q.Set("depth", strconv.Itoa(opts.Depth))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Policy != "" {
		q.Set("policy", opts.Policy)
		if opts.PolicyVersion > 0 {
			q.Set("policy_version", strconv.Itoa(opts.PolicyVersion))
		}
	}
	if opts.MinSimilarity != nil {
		q.Set("min_similarity", strconv.FormatFloat(*opts.MinSimilarity, 'f', -1, 64))
	}
	p := "/v1/assertions/" + url.PathEscape(id) + "/evidence-graph"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	return a.get(ctx, p)
}

// PublishPolicy calls PUT /v1/assertions/policies/{name} under the
// expectedVersion precondition (0: no version yet).
func (a *AssertionsClient) PublishPolicy(ctx context.Context, name string, definition map[string]any, expectedVersion int) (map[string]any, error) {
	var out map[string]any
	body := map[string]any{"expected_version": expectedVersion, "definition": definition}
	if err := a.c.putJSON(ctx, "/v1/assertions/policies/"+url.PathEscape(name), body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetPolicy calls GET /v1/assertions/policies/{name}; version 0 is the latest.
func (a *AssertionsClient) GetPolicy(ctx context.Context, name string, version int) (map[string]any, error) {
	p := "/v1/assertions/policies/" + url.PathEscape(name)
	if version > 0 {
		p += "?version=" + strconv.Itoa(version)
	}
	return a.get(ctx, p)
}

// ResolveOptions are the optional parameters of Resolve.
type ResolveOptions struct {
	// RecordedAsOf is the recorded time to resolve as of; nil is now.
	RecordedAsOf *int64
	// Policy names a published policy; empty is the built-in policy.
	Policy string
	// PolicyVersion selects a policy version; 0 is the latest.
	PolicyVersion int
}

// Resolve calls GET /v1/assertions/resolve: the value at validAt as known at
// opts.RecordedAsOf, with the verdict for every assertion considered.
func (a *AssertionsClient) Resolve(ctx context.Context, subjectType, subjectID, predicate string, validAt int64, opts ResolveOptions) (map[string]any, error) {
	q := url.Values{}
	q.Set("subject_type", subjectType)
	q.Set("subject_id", subjectID)
	q.Set("predicate", predicate)
	q.Set("valid_at", strconv.FormatInt(validAt, 10))
	if opts.RecordedAsOf != nil {
		q.Set("recorded_as_of", strconv.FormatInt(*opts.RecordedAsOf, 10))
	}
	if opts.Policy != "" {
		q.Set("policy", opts.Policy)
		if opts.PolicyVersion > 0 {
			q.Set("policy_version", strconv.Itoa(opts.PolicyVersion))
		}
	}
	return a.get(ctx, "/v1/assertions/resolve?"+q.Encode())
}
