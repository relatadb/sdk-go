package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Memory is a Mem0-style high-level client over the governed /memory/* verbs
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
	policy      string
	summary     string
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

// WithPolicy selects the resolver policy on Resolve ("latest_wins",
// "highest_confidence", "manual"). Defaults to "latest_wins".
func WithPolicy(p string) MemoryOption {
	return func(cfg *memoryCall) { cfg.policy = p }
}

// WithSummaryContent lets the caller supply the summary text on Summarise.
func WithSummaryContent(s string) MemoryOption {
	return func(cfg *memoryCall) { cfg.summary = s }
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
	if err := m.c.postJSON(ctx, "/memory/remember", payload, &resp); err != nil {
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
	if err := m.c.postJSON(ctx, "/memory/remember/batch", payload, &resp); err != nil {
		return nil, err
	}
	return unwrapBatchIDs(unwrapMCP(resp)), nil
}

// Search recalls memories ranked by confidence × recency × relevance. Returns
// the ranked rows (each with id, content, confidence, score, memory_class).
func (m *Memory) Search(ctx context.Context, query string, opts ...MemoryOption) ([]map[string]any, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	topK := cfg.topK
	if topK <= 0 {
		topK = 5
	}
	params := url.Values{}
	params.Set("q", query)
	params.Set("purpose", m.purpose)
	params.Set("top_k", fmt.Sprintf("%d", topK))
	if sid := m.effectiveSession(cfg.sessionID); sid != "" {
		params.Set("session_id", sid)
	}
	if cfg.asOf != "" {
		params.Set("as_of", cfg.asOf)
	}
	var resp map[string]any
	if err := m.c.get(ctx, "/memory/recall?"+params.Encode(), &resp); err != nil {
		return nil, err
	}
	return unwrapRows(unwrapMCP(resp)), nil
}

// Get fetches a single memory by id. Returns nil when not found.
func (m *Memory) Get(ctx context.Context, memoryID string) (map[string]any, error) {
	path := fmt.Sprintf("/memory/recognize/%s?purpose=%s", url.PathEscape(memoryID), url.QueryEscape(m.purpose))
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
	if err := m.c.postJSON(ctx, "/memory/consolidate", payload, &resp); err != nil {
		return "", err
	}
	return unwrapString(unwrapMCP(resp), "new_id"), nil
}

// Forget schedules a governed retention-policy retract for memoryID. Not a hard
// delete — returns the policy decision (memory_item_id, policy, forget_at_ns).
func (m *Memory) Forget(ctx context.Context, memoryID string) (map[string]any, error) {
	path := fmt.Sprintf("/memory/forget/%s?purpose=%s", url.PathEscape(memoryID), url.QueryEscape(m.purpose))
	var resp map[string]any
	if err := m.c.delete(ctx, path, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// Associate links two memories with a typed relation. Returns the association
// record (source_id, target_id, relation, confidence).
func (m *Memory) Associate(ctx context.Context, sourceID, targetID, relation string, opts ...MemoryOption) (map[string]any, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	payload := map[string]any{
		"source_id":  sourceID,
		"target_id":  targetID,
		"relation":   relation,
		"confidence": clampConfidence(cfg.confidence, 1.0),
		"purpose":    m.purpose,
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/memory/associate", payload, &resp); err != nil {
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
	if err := m.c.get(ctx, "/memory/episodes?"+params.Encode(), &resp); err != nil {
		return nil, err
	}
	return unwrapRows(unwrapMCP(resp)), nil
}

// Justify returns the PROV-O assertion chain for memoryID — the derivation
// lineage (source session, tool call, prior belief, …).
func (m *Memory) Justify(ctx context.Context, memoryID string) (map[string]any, error) {
	path := fmt.Sprintf("/memory/justify/%s?purpose=%s", url.PathEscape(memoryID), url.QueryEscape(m.purpose))
	var resp map[string]any
	if err := m.c.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// Resolve resolves a contradiction between memoryID and a newer belief. The
// policy selects the resolver ("latest_wins" / "highest_confidence" / "manual").
// Returns the resolution record.
func (m *Memory) Resolve(ctx context.Context, memoryID string, opts ...MemoryOption) (map[string]any, error) {
	cfg := applyMemoryOpts(opts, 1.0, "semantic")
	policy := orDefault(cfg.policy, "latest_wins")
	payload := map[string]any{
		"policy":  policy,
		"purpose": m.purpose,
	}
	path := fmt.Sprintf("/memory/resolve/%s", url.PathEscape(memoryID))
	var resp map[string]any
	if err := m.c.postJSON(ctx, path, payload, &resp); err != nil {
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
		"source_ids": sourceIDs,
		"purpose":    m.purpose,
	}
	if cfg.summary != "" {
		payload["summary_content"] = cfg.summary
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/memory/summarise", payload, &resp); err != nil {
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
	url := c.baseURL + path
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
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, nil, fmt.Errorf("relata: read body: %w", readErr)
	}
	hdrs := make(map[string]string)
	for k := range resp.Header {
		hdrs[k] = resp.Header.Get(k)
	}
	return resp.StatusCode, data, hdrs, nil
}
