package relata

import (
	"context"
	"net/url"
	"strconv"
)

// ExtractionClient wraps /ontology/suggestions and /entities/exists (#5678).
// OpenETL extracts entities, relationships and schema proposals; Relata stores
// them. Submit a bounded batch (every suggestion starts pending; a submission
// cannot carry a review state), work the review queue with List and Get, and
// Accept, Reject, Correct or Reopen each one. The original payload is kept
// beside the correction and every decision. Accepted schema suggestions fold
// into a definition draft with Apply, to be published as the next ontology
// version. EntitiesExist answers, for up to 500 candidates, which already exist
// and with which ids. Every ontology read returns an ontology_version token;
// pass it back on a suggestion and the queue marks it stale once the ontology
// has moved on.
type ExtractionClient struct {
	c *Client
}

// NewExtractionClient inherits the parent client's auth and tenant.
func NewExtractionClient(c *Client) *ExtractionClient {
	return &ExtractionClient{c: c}
}

func (e *ExtractionClient) get(ctx context.Context, path string) (map[string]any, error) {
	var out map[string]any
	if err := e.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *ExtractionClient) post(ctx context.Context, path string, in any) (map[string]any, error) {
	var out map[string]any
	if err := e.c.postJSON(ctx, path, in, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Submit calls POST /ontology/suggestions with 1 to 200 suggestions; each
// result is created, existing (an exact retry) or invalid with issues.
func (e *ExtractionClient) Submit(ctx context.Context, suggestions []map[string]any) (map[string]any, error) {
	return e.post(ctx, "/ontology/suggestions", map[string]any{"suggestions": suggestions})
}

// SuggestionFilter narrows List; zero values are omitted. LowConfidence and
// Stale are tri-state: nil leaves the filter off.
type SuggestionFilter struct {
	State         string
	Kind          string
	Definition    string
	LowConfidence *bool
	Stale         *bool
	MinConfidence *float64
	Limit         int
}

// List calls GET /ontology/suggestions: the review queue, oldest first.
func (e *ExtractionClient) List(ctx context.Context, f SuggestionFilter) (map[string]any, error) {
	q := url.Values{}
	for k, v := range map[string]string{"state": f.State, "kind": f.Kind, "definition": f.Definition} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.LowConfidence != nil {
		q.Set("low_confidence", strconv.FormatBool(*f.LowConfidence))
	}
	if f.Stale != nil {
		q.Set("stale", strconv.FormatBool(*f.Stale))
	}
	if f.MinConfidence != nil {
		q.Set("min_confidence", strconv.FormatFloat(*f.MinConfidence, 'f', -1, 64))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	p := "/ontology/suggestions"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	return e.get(ctx, p)
}

// Get calls GET /ontology/suggestions/{id}: with its correction and history.
func (e *ExtractionClient) Get(ctx context.Context, id string) (map[string]any, error) {
	return e.get(ctx, "/ontology/suggestions/"+url.PathEscape(id))
}

// Decide calls POST .../decision with "accept", "reject", "correct" or
// "reopen"; idempotent. correction is required with "correct".
func (e *ExtractionClient) Decide(ctx context.Context, id, decision, reason string, correction map[string]any, acceptLowConfidence bool) (map[string]any, error) {
	body := map[string]any{"decision": decision, "reason": reason}
	if correction != nil {
		body["correction"] = correction
	}
	if acceptLowConfidence {
		body["accept_low_confidence"] = true
	}
	return e.post(ctx, "/ontology/suggestions/"+url.PathEscape(id)+"/decision", body)
}

// Accept accepts a suggestion as extracted. A low-confidence suggestion needs
// acceptLowConfidence.
func (e *ExtractionClient) Accept(ctx context.Context, id, reason string, acceptLowConfidence bool) (map[string]any, error) {
	return e.Decide(ctx, id, "accept", reason, nil, acceptLowConfidence)
}

// Reject rejects a suggestion; a reprocessed run of the same claim inherits it.
func (e *ExtractionClient) Reject(ctx context.Context, id, reason string) (map[string]any, error) {
	return e.Decide(ctx, id, "reject", reason, nil, false)
}

// Correct amends a suggestion's payload; the original is kept.
func (e *ExtractionClient) Correct(ctx context.Context, id string, correction map[string]any, reason string) (map[string]any, error) {
	return e.Decide(ctx, id, "correct", reason, correction, false)
}

// Reopen returns a decided suggestion to pending.
func (e *ExtractionClient) Reopen(ctx context.Context, id, reason string) (map[string]any, error) {
	return e.Decide(ctx, id, "reopen", reason, nil, false)
}

// Apply calls POST /ontology/suggestions/apply: fold accepted schema
// suggestions into the definition's draft. suggestionIDs nil means all.
func (e *ExtractionClient) Apply(ctx context.Context, definition string, suggestionIDs []string) (map[string]any, error) {
	body := map[string]any{"definition": definition}
	if suggestionIDs != nil {
		body["suggestion_ids"] = suggestionIDs
	}
	return e.post(ctx, "/ontology/suggestions/apply", body)
}

// EntitiesExist calls POST /entities/exists: for up to 500 candidates of the
// form {"type": ..., "match": {field: value}}, whether a live record matches
// exactly and with which ids. A non-empty definition also returns its current
// ontology_version.
func (e *ExtractionClient) EntitiesExist(ctx context.Context, candidates []map[string]any, definition string) (map[string]any, error) {
	body := map[string]any{"candidates": candidates}
	if definition != "" {
		body["definition"] = definition
	}
	return e.post(ctx, "/entities/exists", body)
}
