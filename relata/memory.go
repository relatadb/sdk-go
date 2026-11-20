package relata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Memory is a Mem0-style high-level client over the governed /v1/memory/* verbs
// (ADR-144). It owns its own HTTP transport (a private *Client) so it does not
// depend on a caller-constructed Client. A non-empty purpose is required — the
// server records it for audit and enforces ACL on every call.
//
// Example:
//
//	m, _ := relata.NewMemory("http://localhost:9090", "agent-notes",
//	    &relata.MemoryOptions{BearerToken: token})
//	id, _ := m.Add(ctx, "Alice prefers dark mode")
//	hits, _ := m.Search(ctx, "ui preferences", relata.WithTopK(5))
//	m.Forget(ctx, id)
type Memory struct {
	c         *Client
	purpose   string
	sessionID string
}

// MemoryOption is a functional option applied to a single Memory call.
// Options override the defaults configured on MemoryOptions.
type MemoryOption func(*memoryCall)

// memoryCall holds the per-call optional settings resolved from MemoryOptions.
type memoryCall struct {
	confidence  float64
	memoryClass string
	sessionID   string
	topK        int
	asOf        string
	summary     string

	// ADR-145 retrieval-quality operators (#2674). Pointers so "unset" (send
	// nothing, let the server default) is distinguishable from a meaningful
	// zero value (e.g. budgetTokens=0 or minConfidence=0).
	minConfidence       *float64
	recencyHalfLifeSecs *float64
	budgetTokens        *uint64
	stabilityDays       *float64
	cancelThreshold     *float64
}

// WithConfidence sets the confidence (clamped to [0,1]) on Add/Associate.
func WithConfidence(c float64) MemoryOption {
	return func(cfg *memoryCall) { cfg.confidence = c }
}

// WithMemoryClass sets the memory class ("episodic", "semantic", "procedural")
// on Add. Defaults to "semantic".
func WithMemoryClass(class string) MemoryOption {
	return func(cfg *memoryCall) { cfg.memoryClass = class }
}

// WithSessionID overrides the default session id for a single Add/AddBatch/
// Search/Episodes call.
func WithSessionID(id string) MemoryOption {
	return func(cfg *memoryCall) { cfg.sessionID = id }
}

// WithTopK sets the result limit on Search/Recall. Defaults to 5.
func WithTopK(k int) MemoryOption {
	return func(cfg *memoryCall) { cfg.topK = k }
}

// WithAsOf makes a Search/Episodes lookup bi-temporal at the given timestamp.
func WithAsOf(ts string) MemoryOption {
	return func(cfg *memoryCall) { cfg.asOf = ts }
}

// WithSummaryContent lets the caller supply the summary text on Summarise.
func WithSummaryContent(s string) MemoryOption {
	return func(cfg *memoryCall) { cfg.summary = s }
}

// WithMinConfidence is ADR-145's CONFIDENCE(f) operator: on Search/
// SearchDetailed, filters results to rows with confidence >= f.
func WithMinConfidence(f float64) MemoryOption {
	return func(cfg *memoryCall) { cfg.minConfidence = &f }
}

// WithRecencyHalfLife is ADR-145's RECENCY(λ) operator: on Search/
// SearchDetailed, applies exponential score decay by row age with the given
// half-life in seconds.
func WithRecencyHalfLife(secs float64) MemoryOption {
	return func(cfg *memoryCall) { cfg.recencyHalfLifeSecs = &secs }
}

// WithBudgetTokens is ADR-145's BUDGET(t) operator: on Search/SearchDetailed,
// stops result emission once cumulative recall_cost_tokens exceeds t.
func WithBudgetTokens(t uint64) MemoryOption {
	return func(cfg *memoryCall) { cfg.budgetTokens = &t }
}

// WithStabilityDays is ADR-145's FORGETTING_CURVE(d) operator: on Search/
// SearchDetailed, weights retrieval score by the Ebbinghaus retention
// fraction using the given stability parameter, in days.
func WithStabilityDays(d float64) MemoryOption {
	return func(cfg *memoryCall) { cfg.stabilityDays = &d }
}

// WithCancelThreshold is ADR-145's CANCEL_WHEN(threshold) operator: on
// Search/SearchDetailed, short-circuits the scan once any result exceeds
// threshold. SearchDetailed's Cancelled field reports whether this fired.
func WithCancelThreshold(threshold float64) MemoryOption {
	return func(cfg *memoryCall) { cfg.cancelThreshold = &threshold }
}

// NewMemory constructs a standalone Memory client. purpose must be non-empty
// (governance is on); an empty purpose returns ErrPurposeRequired.
//
// The Memory client owns its own *http.Client (it does not depend on a
// caller-constructed Client), mirroring the Python reference where Memory has
// its own transport.
func NewMemory(baseURL, purpose string, opts *MemoryOptions) (*Memory, error) {
	if strings.TrimSpace(purpose) == "" {
		return nil, ErrPurposeRequired
	}
	if opts == nil {
		opts = &MemoryOptions{}
	}
	hdrs := map[string]string{}
	if opts.Tenant != "" {
		hdrs[headerTenant] = opts.Tenant
	}
	for k, v := range opts.Headers {
		hdrs[k] = v
	}
	clientOpts := &ClientOptions{
		BearerToken: opts.BearerToken,
		Timeout:     opts.Timeout,
		HTTPClient:  opts.HTTPClient,
		Headers:     hdrs,
	}
	m := &Memory{
		c:         New(baseURL, clientOpts),
		purpose:   purpose,
		sessionID: opts.SessionID,
	}
	return m, nil
}

// Add stores a memory and returns its id. memoryClass is one of "episodic",
// "semantic", or "procedural" (defaults to "semantic"); confidence is clamped
// to [0, 1].
func (m *Memory) Add(ctx context.Context, content string, opts ...MemoryOption) (string, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	payload := map[string]any{
		"content":      content,
		"purpose":      m.purpose,
		"confidence":   clampConfidence(cfg.confidence, 1.0),
		"memory_class": orDefault(cfg.memoryClass, "semantic"),
		"session_id":   m.effectiveSession(cfg.sessionID),
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/remember", payload, &resp); err != nil {
		return "", err
	}
	return unwrapString(unwrapMCP(resp), "id"), nil
}

// AddBatch stores many memories in one request and returns their ids in
// submission order. Each item is either a plain string (content) or a map with
// "content" and optional "confidence" / "memory_class" / "session_id" /
// "purpose". Items that fail server-side validation yield "" at their position;
// valid items still commit.
func (m *Memory) AddBatch(ctx context.Context, items []any, opts ...MemoryOption) ([]string, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	defaultSession := m.effectiveSession(cfg.sessionID)
	normalised := normaliseBatchItems(items, defaultSession)
	payload := map[string]any{
		"purpose": m.purpose,
		"items":   normalised,
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/remember/batch", payload, &resp); err != nil {
		return nil, err
	}
	return unwrapBatchIDs(unwrapMCP(resp)), nil
}

// RecallResult is the full ADR-145 recall envelope returned by
// SearchDetailed: the ranked rows plus the read-only RecallCostTokens
// (BUDGET's running token total) and Cancelled (CANCEL_WHEN short-circuit
// flag) fields, so callers can observe the effect of the retrieval-quality
// options, not just set them.
type RecallResult struct {
	Rows             []map[string]any
	RecallCostTokens uint64
	Cancelled        bool
	// Embedding is the recall's query-embedding flag (#5726).
	Embedding *EmbeddingFlag
}

// recallBody builds the POST /v1/memory/recall JSON body shared by Search,
// SearchDetailed and SearchStream, including the five ADR-145 retrieval-quality
// operators (WithMinConfidence/WithRecencyHalfLife/WithBudgetTokens/
// WithStabilityDays/WithCancelThreshold). Each is omitted when unset so the
// server applies its own default.
func (m *Memory) recallBody(query string, cfg memoryCall) map[string]any {
	topK := cfg.topK
	if topK <= 0 {
		topK = 5
	}
	body := map[string]any{
		"query":   query,
		"purpose": m.purpose,
		"top_k":   topK,
	}
	if sid := m.effectiveSession(cfg.sessionID); sid != "" {
		body["session_id"] = sid
	}
	if cfg.asOf != "" {
		body["as_of"] = cfg.asOf
	}
	if cfg.minConfidence != nil {
		body["min_confidence"] = *cfg.minConfidence
	}
	if cfg.recencyHalfLifeSecs != nil {
		body["recency_half_life_secs"] = *cfg.recencyHalfLifeSecs
	}
	if cfg.budgetTokens != nil {
		body["budget_tokens"] = *cfg.budgetTokens
	}
	if cfg.stabilityDays != nil {
		body["stability_days"] = *cfg.stabilityDays
	}
	if cfg.cancelThreshold != nil {
		body["cancel_threshold"] = *cfg.cancelThreshold
	}
	return body
}

// Search recalls memories ranked by confidence × recency × relevance. Returns
// the ranked rows (each with id, content, confidence, score, memory_class).
//
// The ADR-145 retrieval-quality operators (WithMinConfidence/
// WithRecencyHalfLife/WithBudgetTokens/WithStabilityDays/
// WithCancelThreshold) narrow or bound the recall; use SearchDetailed
// instead of Search to also observe the read-only RecallCostTokens/
// Cancelled fields these options produce.
func (m *Memory) Search(ctx context.Context, query string, opts ...MemoryOption) ([]map[string]any, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/recall", m.recallBody(query, cfg), &resp); err != nil {
		return nil, err
	}
	inner := unwrapMCP(resp)
	warnIfLexical(embeddingFlagFromMap(inner)) // #5726
	return unwrapRows(inner), nil
}

// SearchDetailed is like Search but returns the full RecallResult, including
// the read-only RecallCostTokens (BUDGET) and Cancelled (CANCEL_WHEN) fields
// alongside Rows.
func (m *Memory) SearchDetailed(ctx context.Context, query string, opts ...MemoryOption) (RecallResult, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/recall", m.recallBody(query, cfg), &resp); err != nil {
		return RecallResult{}, err
	}
	inner := unwrapMCP(resp)
	result := RecallResult{Rows: unwrapRows(inner), Embedding: embeddingFlagFromMap(inner)}
	warnIfLexical(result.Embedding) // #5726
	if v, ok := inner["recall_cost_tokens"].(float64); ok {
		result.RecallCostTokens = uint64(v)
	}
	if v, ok := inner["cancelled"].(bool); ok {
		result.Cancelled = v
	}
	return result, nil
}

// SearchStream runs a recall as a server-sent-event stream
// (GET /v1/memory/recall/stream) and returns the raw text/event-stream body.
// Each "data:" frame is a JSON object with a "type" of meta, memory, done or
// error. The caller must Close the returned reader. The stream endpoint takes
// the same retrieval options as Search, sent as query parameters.
func (m *Memory) SearchStream(ctx context.Context, query string, opts ...MemoryOption) (io.ReadCloser, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	params := url.Values{}
	for k, v := range m.recallBody(query, cfg) {
		params.Set(k, fmt.Sprintf("%v", v))
	}
	return m.c.openStream(ctx, http.MethodGet, "/v1/memory/recall/stream?"+params.Encode(), nil, "")
}

// RememberProcedure stores a versioned agent procedure
// (POST /v1/memory/remember-procedure) and returns the decoded result.
func (m *Memory) RememberProcedure(ctx context.Context, agentID, name, instructionText string) (map[string]any, error) {
	payload := map[string]any{
		"agent_id":         agentID,
		"name":             name,
		"instruction_text": instructionText,
		"purpose":          m.purpose,
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/remember-procedure", payload, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// RecallProcedure reads back stored procedures for an agent
// (GET /v1/memory/recall-procedure) and returns the decoded result.
func (m *Memory) RecallProcedure(ctx context.Context, agentID string, opts *RecallProcedureOptions) (map[string]any, error) {
	params := map[string]string{"agent_id": agentID, "purpose": m.purpose, "limit": "20"}
	if opts != nil {
		if opts.Name != "" {
			params["name"] = opts.Name
		}
		if opts.AllVersions {
			params["all_versions"] = "true"
		}
		if opts.Limit > 0 {
			params["limit"] = fmt.Sprintf("%d", opts.Limit)
		}
		if opts.Purpose != "" {
			params["purpose"] = opts.Purpose
		}
	}
	var resp map[string]any
	if err := m.c.get(ctx, encodeGetURL("/v1/memory/recall-procedure", params), &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// Get fetches a single memory by id. Returns nil when not found.
func (m *Memory) Get(ctx context.Context, memoryID string) (map[string]any, error) {
	path := fmt.Sprintf("/v1/memory/recognize/%s?purpose=%s", url.PathEscape(memoryID), url.QueryEscape(m.purpose))
	var resp map[string]any
	if err := m.c.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	inner := unwrapMCP(resp)
	if mem, ok := inner["memory"].(map[string]any); ok {
		return mem, nil
	}
	return nil, nil
}

// Update supersedes memoryID with new content (governed) and returns the new id.
func (m *Memory) Update(ctx context.Context, memoryID, content string) (string, error) {
	payload := map[string]any{
		"id":      memoryID,
		"content": content,
		"purpose": m.purpose,
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/consolidate", payload, &resp); err != nil {
		return "", err
	}
	return unwrapString(unwrapMCP(resp), "new_id"), nil
}

// Forget schedules a governed retention-policy retract for memoryID. Not a hard
// delete — returns the policy decision (memory_item_id, policy, forget_at_ns).
func (m *Memory) Forget(ctx context.Context, memoryID string) (map[string]any, error) {
	path := fmt.Sprintf("/v1/memory/forget/%s?purpose=%s", url.PathEscape(memoryID), url.QueryEscape(m.purpose))
	var resp map[string]any
	if err := m.c.delete(ctx, path, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// Review approves or rejects a pending memory; decision is "approve" or
// "reject" and reason is optional. Wraps POST /v1/memory/review/{id}: memory stored
// with an external: origin is pending and hidden from recall until reviewed.
// Returns id, review_status, previous_review_status and changed.
func (m *Memory) Review(ctx context.Context, memoryID, decision, reason string) (map[string]any, error) {
	payload := map[string]any{"decision": decision, "purpose": m.purpose}
	if reason != "" {
		payload["reason"] = reason
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/review/"+url.PathEscape(memoryID), payload, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// Associate links two memories with a typed relation. The server reads
// from_id / to_id / relation; confidence options are accepted for source
// compatibility but are not part of the server schema and are ignored.
// Returns the association record.
func (m *Memory) Associate(ctx context.Context, sourceID, targetID, relation string, _ ...MemoryOption) (map[string]any, error) {
	payload := map[string]any{
		"from_id":  sourceID,
		"to_id":    targetID,
		"relation": relation,
		"purpose":  m.purpose,
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/associate", payload, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// Episodes lists episodes with full Episode detail. An optional session_id
// filters to one session; as_of makes the lookup bi-temporal.
func (m *Memory) Episodes(ctx context.Context, opts ...MemoryOption) ([]map[string]any, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	params := url.Values{}
	params.Set("purpose", m.purpose)
	if sid := m.effectiveSession(cfg.sessionID); sid != "" {
		params.Set("session_id", sid)
	}
	if cfg.asOf != "" {
		params.Set("as_of", cfg.asOf)
	}
	var resp map[string]any
	if err := m.c.get(ctx, "/v1/memory/episodes?"+params.Encode(), &resp); err != nil {
		return nil, err
	}
	return unwrapRows(unwrapMCP(resp)), nil
}

// Justify returns the PROV-O assertion chain for memoryID — the derivation
// lineage (source session, tool call, prior belief, …).
func (m *Memory) Justify(ctx context.Context, memoryID string) (map[string]any, error) {
	path := fmt.Sprintf("/v1/memory/justify/%s?purpose=%s", url.PathEscape(memoryID), url.QueryEscape(m.purpose))
	var resp map[string]any
	if err := m.c.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// Resolve resolves memoryID's supersession chain to its canonical head.
// Wraps GET /v1/memory/resolve/:id; the server route is GET-only and does not
// read a policy field (#2675).
func (m *Memory) Resolve(ctx context.Context, memoryID string) (map[string]any, error) {
	path := fmt.Sprintf("/v1/memory/resolve/%s?purpose=%s", url.PathEscape(memoryID), url.QueryEscape(m.purpose))
	var resp map[string]any
	if err := m.c.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// AssociateBatch links many memory pairs in one request (POST
// /v1/memory/associate/batch). Each item is {"from_id", "to_id", "relation"?,
// "confidence"?, "purpose"?}; the client's purpose fills any item that omits
// it. The result is {"stored", "submitted", "results"}; stored below submitted
// means some items were rejected (see results).
func (m *Memory) AssociateBatch(ctx context.Context, items []map[string]any) (map[string]any, error) {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if s, _ := item["from_id"].(string); s == "" {
			return nil, fmt.Errorf("relata: each association needs non-empty from_id and to_id")
		}
		if s, _ := item["to_id"].(string); s == "" {
			return nil, fmt.Errorf("relata: each association needs non-empty from_id and to_id")
		}
		merged := map[string]any{"purpose": m.purpose}
		for k, v := range item {
			merged[k] = v
		}
		out = append(out, merged)
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/associate/batch", map[string]any{"items": out}, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// Summarise produces a summary belief from a set of source memories. When
// summaryContent is supplied (via WithSummaryContent) the server stores that
// text; otherwise it derives one. Returns the new summary memory.
func (m *Memory) Summarise(ctx context.Context, sourceIDs []string, opts ...MemoryOption) (map[string]any, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	payload := map[string]any{
		"ids":     sourceIDs,
		"purpose": m.purpose,
	}
	if cfg.summary != "" {
		payload["contents"] = []string{cfg.summary}
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/v1/memory/summarise", payload, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// ---- unexported helpers -----------------------------------------------------

// effectiveSession returns the per-call session id when set, else the
// Memory-level default.
func (m *Memory) effectiveSession(callSession string) string {
	if callSession != "" {
		return callSession
	}
	return m.sessionID
}

// applyMemoryOpts resolves a slice of MemoryOptions into a memoryCall, applying
// the given defaults for confidence and memory class.
func applyMemoryOpts(opts []MemoryOption, defaultConf float64, defaultClass string) memoryCall {
	cfg := memoryCall{confidence: defaultConf, memoryClass: defaultClass}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// clampConfidence clamps v to [0, 1], falling back to def when v is the zero
// value (0.0) — matching the Python default of 1.0.
func clampConfidence(v, def float64) float64 {
	if v == 0 {
		return def
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// orDefault returns s when non-empty, else def.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// unwrapMCP unwraps the MCP envelope the memory + MCP verbs return.
//
// The server replies {"content": [{"type": "text", "text": "<json>"}],
// "isError": false} where the actual result is JSON encoded inside
// content[0].text. Returns the decoded inner object, or the response unchanged
// when it is already flat.
func unwrapMCP(resp map[string]any) map[string]any {
	content, ok := resp["content"].([]any)
	if !ok || len(content) == 0 {
		return resp
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		return resp
	}
	text, ok := first["text"].(string)
	if !ok {
		return resp
	}
	var inner any
	if err := json.Unmarshal([]byte(text), &inner); err != nil {
		return map[string]any{"text": text}
	}
	switch v := inner.(type) {
	case map[string]any:
		return v
	default:
		return map[string]any{"value": v}
	}
}

// unwrapString pulls a string field out of an unwrapped response.
func unwrapString(resp map[string]any, key string) string {
	if v, ok := resp[key].(string); ok {
		return v
	}
	return ""
}

// unwrapRows pulls the "rows" array out of an unwrapped response.
func unwrapRows(resp map[string]any) []map[string]any {
	rows, ok := resp["rows"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// unwrapList pulls a top-level array field out of an unwrapped response.
func unwrapList(resp map[string]any, key string) []map[string]any {
	arr, ok := resp[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// unwrapBatchIDs pulls the per-item ids out of a remember_batch response.
// Positions that failed validation carry no "id" and yield "" so the returned
// list stays index-aligned with the submitted items.
func unwrapBatchIDs(resp map[string]any) []string {
	results, ok := resp["results"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(results))
	for _, r := range results {
		m, ok := r.(map[string]any)
		if !ok {
			out = append(out, "")
			continue
		}
		if id, ok := m["id"].(string); ok {
			out = append(out, id)
		} else {
			out = append(out, "")
		}
	}
	return out
}

// normaliseBatchItems turns AddBatch inputs (strings or maps) into remember_batch
// item dicts, defaulting the session id when missing.
func normaliseBatchItems(items []any, defaultSession string) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			out = append(out, map[string]any{"content": v, "session_id": defaultSession})
		case map[string]any:
			d := make(map[string]any, len(v))
			for k, val := range v {
				d[k] = val
			}
			if _, ok := d["session_id"]; !ok {
				d["session_id"] = defaultSession
			}
			out = append(out, d)
		default:
			out = append(out, map[string]any{"content": fmt.Sprintf("%v", v), "session_id": defaultSession})
		}
	}
	return out
}

// encodeGetURL is a tiny helper for clients that build GET query strings.
func encodeGetURL(base string, params map[string]string) string {
	v := url.Values{}
	for k, val := range params {
		if val == "" {
			continue
		}
		v.Set(k, val)
	}
	enc := v.Encode()
	if enc == "" {
		return base
	}
	return base + "?" + enc
}

// rawHTTPRequest dispatches a request using the shared http client without
// JSON-decoding the response; returns the raw status code + body bytes + error.
// Used by the audit PDF export path which returns binary content.
func (c *Client) rawHTTPRequest(ctx context.Context, method, path string, body []byte, contentType string, extraHeaders ...map[string]string) (int, []byte, map[string]string, error) {
	if c.destroyed {
		return 0, nil, nil, ErrClientClosed
	}
	url := c.baseURL + path
	if err := c.cleartextBearerGuard(url); err != nil {
		return 0, nil, nil, err
	}
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(body)))
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return 0, nil, nil, fmt.Errorf("relata: build request: %w", err)
	}
	if contentType != "" {
		req.Header.Set(headerContentType, contentType)
	}
	c.setHeaders(req)
	for _, hdrs := range extraHeaders {
		for k, v := range hdrs {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, c.classifyTransportError(ctx, err)
	}
	defer resp.Body.Close()
	data, readErr := c.readCapped(resp.Body)
	if readErr != nil {
		if errors.Is(readErr, ErrResponseTooLarge) {
			return resp.StatusCode, nil, nil, readErr
		}
		return resp.StatusCode, nil, nil, fmt.Errorf("relata: read body: %w", readErr)
	}
	hdrs := make(map[string]string)
	for k := range resp.Header {
		hdrs[k] = resp.Header.Get(k)
	}
	return resp.StatusCode, data, hdrs, nil
}
