package relata

import (
	"context"
	"net/url"
	"strconv"
)

// InvestigationClient wraps /alerts/{id}/{evidence,timeline,pivot} and
// /searches/saved (#5702). AlertEvidence returns the source events behind an
// alert with a reproduction query, completeness report and SHA-256.
// AlertTimeline orders the same facts (window, events, firing, triage changes)
// and lists the entities to pivot on. Pivot follows one of those entities into
// the caller's other readable records; the value must come from evidence the
// caller could read, and unreadable types and rows are omitted without a
// count. The saved-search methods keep a named SQL query (versioned, per
// tenant, durable); RunSavedSearch runs it through the governed POST /query
// path with the caller's own read policy, so saving a query grants nothing.
type InvestigationClient struct {
	c *Client
}

// NewInvestigationClient inherits the parent client's auth and tenant.
func NewInvestigationClient(c *Client) *InvestigationClient {
	return &InvestigationClient{c: c}
}

func (i *InvestigationClient) get(ctx context.Context, path string) (map[string]any, error) {
	var out map[string]any
	if err := i.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (i *InvestigationClient) post(ctx context.Context, path string, in any) (map[string]any, error) {
	var out map[string]any
	if err := i.c.postJSON(ctx, path, in, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EvidenceBounds limits an evidence or timeline fetch; zero values are omitted.
type EvidenceBounds struct {
	MaxEvents  int
	DeadlineMs int
}

func (b EvidenceBounds) query() string {
	q := url.Values{}
	if b.MaxEvents > 0 {
		q.Set("max_events", strconv.Itoa(b.MaxEvents))
	}
	if b.DeadlineMs > 0 {
		q.Set("deadline_ms", strconv.Itoa(b.DeadlineMs))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// AlertEvidence calls GET /alerts/{id}/evidence.
func (i *InvestigationClient) AlertEvidence(ctx context.Context, alertID string, b EvidenceBounds) (map[string]any, error) {
	return i.get(ctx, "/alerts/"+url.PathEscape(alertID)+"/evidence"+b.query())
}

// AlertTimeline calls GET /alerts/{id}/timeline: ordered entries, unplaced ids
// and the entities to pivot on.
func (i *InvestigationClient) AlertTimeline(ctx context.Context, alertID string, b EvidenceBounds) (map[string]any, error) {
	return i.get(ctx, "/alerts/"+url.PathEscape(alertID)+"/timeline"+b.query())
}

// PivotRequest selects the entity to follow; zero values are omitted. Value is
// optional when the evidence holds exactly one value of Field.
type PivotRequest struct {
	Field       string
	Value       any
	TargetField string
	TargetTypes []string
	Limit       int
	DeadlineMs  int
}

// Pivot calls POST /alerts/{id}/pivot: other readable records for one evidence
// entity, reported as partial when a bound was hit.
func (i *InvestigationClient) Pivot(ctx context.Context, alertID string, r PivotRequest) (map[string]any, error) {
	body := map[string]any{}
	if r.Field != "" {
		body["field"] = r.Field
	}
	if r.Value != nil {
		body["value"] = r.Value
	}
	if r.TargetField != "" {
		body["target_field"] = r.TargetField
	}
	if r.TargetTypes != nil {
		body["target_types"] = r.TargetTypes
	}
	if r.Limit > 0 {
		body["limit"] = r.Limit
	}
	if r.DeadlineMs > 0 {
		body["deadline_ms"] = r.DeadlineMs
	}
	return i.post(ctx, "/alerts/"+url.PathEscape(alertID)+"/pivot", body)
}

// SavedSearch is a saved-search definition; empty optional fields are omitted.
type SavedSearch struct {
	Name        string
	SQL         string
	Purpose     string
	Description string
	Params      []any
}

func (s SavedSearch) body(expectedVersion int) map[string]any {
	b := map[string]any{"name": s.Name, "sql": s.SQL}
	if s.Purpose != "" {
		b["purpose"] = s.Purpose
	}
	if s.Description != "" {
		b["description"] = s.Description
	}
	if s.Params != nil {
		b["params"] = s.Params
	}
	if expectedVersion > 0 {
		b["expected_version"] = expectedVersion
	}
	return b
}

// CreateSavedSearch calls POST /searches/saved; a taken name is 409
// saved-search-exists.
func (i *InvestigationClient) CreateSavedSearch(ctx context.Context, s SavedSearch) (map[string]any, error) {
	return i.post(ctx, "/searches/saved", s.body(0))
}

// ListSavedSearches calls GET /searches/saved: {items, total} ordered by name.
// An empty q and a zero limit are omitted.
func (i *InvestigationClient) ListSavedSearches(ctx context.Context, q string, limit int) (map[string]any, error) {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	p := "/searches/saved"
	if len(v) > 0 {
		p += "?" + v.Encode()
	}
	return i.get(ctx, p)
}

// GetSavedSearch calls GET /searches/saved/{id}.
func (i *InvestigationClient) GetSavedSearch(ctx context.Context, id string) (map[string]any, error) {
	return i.get(ctx, "/searches/saved/"+url.PathEscape(id))
}

// UpdateSavedSearch calls PUT /searches/saved/{id}. A positive expectedVersion
// makes the update conditional (409 saved-search-version-conflict).
func (i *InvestigationClient) UpdateSavedSearch(ctx context.Context, id string, s SavedSearch, expectedVersion int) (map[string]any, error) {
	var out map[string]any
	if err := i.c.putJSON(ctx, "/searches/saved/"+url.PathEscape(id), s.body(expectedVersion), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteSavedSearch calls DELETE /searches/saved/{id}: it leaves listings and
// the store keeps the history.
func (i *InvestigationClient) DeleteSavedSearch(ctx context.Context, id string) (map[string]any, error) {
	var out map[string]any
	if err := i.c.delete(ctx, "/searches/saved/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RunSavedSearch calls POST /searches/saved/{id}/run and returns the POST
// /query response for the stored SQL. A nil params keeps the stored bindings.
func (i *InvestigationClient) RunSavedSearch(ctx context.Context, id string, params []any) (map[string]any, error) {
	body := map[string]any{}
	if params != nil {
		body["params"] = params
	}
	return i.post(ctx, "/searches/saved/"+url.PathEscape(id)+"/run", body)
}
