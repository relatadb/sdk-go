// Package relata provides a Go client for the Relata data engine.
//
// Relata is an ontology-driven, enterprise-grade data engine. Every query
// MUST declare a purpose token registered in the tenant's PurposeRegistry;
// queries that omit a purpose are rejected at the wire.
//
// Basic usage:
//
//	client := relata.New("http://localhost:9090", &relata.ClientOptions{
//	    BearerToken:    os.Getenv("RELATA_TOKEN"),
//	    DefaultPurpose: "investigation",
//	})
//
//	result, err := client.Query(ctx, "SELECT * FROM Person LIMIT 10")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	result.Each(func(row map[string]any) {
//	    fmt.Println(row)
//	})
package relata

import (
	"encoding/json"
	"net/http"
	"time"
)

// QueryRequest is the wire payload sent to POST /query.
type QueryRequest struct {
	// Purpose is a mandatory token registered in the tenant's PurposeRegistry.
	// The server rejects any query that omits this field.
	Purpose string `json:"purpose"`

	// SQL is the query string. Relata extends standard SQL with:
	//   AS OF 'timestamp'          — bi-temporal point-in-time reads
	//   WITH PROVENANCE            — attach PROV-O lineage to every row
	//   PATHS_BETWEEN(a, b, ...)   — CSR graph traversal
	//   MATCH_FACE(...)            — biometric face search
	//   LOOKUP_IDENTITY(...)       — IdentityIndex resolution
	//   HYBRID_SCORE(...)          — combined BM25 + vector ranking
	SQL string `json:"sql"`

	// Params holds positional bind values for $1, $2, … placeholders (#1162).
	// Omit (nil) when the SQL has no placeholders.
	Params []any `json:"params,omitempty"`
}

// QueryResult is the response from POST /query.
//
// The server returns one of two wire shapes: either rows-as-an-int-count with
// the actual row data in a sibling "data" array, or rows-as-the-array directly.
// UnmarshalJSON normalises both into Rows so callers never have to branch.
type QueryResult struct {
	// Rows holds the result set. Each row is a map from column name to value.
	// Values are decoded from JSON: numbers arrive as float64 unless the column
	// type is declared as integer in the ontology, in which case int64 is used.
	Rows []map[string]any `json:"rows"`

	// QueryID is the server-assigned opaque identifier for this query execution.
	// It appears in the audit trail and can be used to correlate log entries.
	QueryID string `json:"query_id"`

	// ElapsedMs is the server-side wall-clock time in milliseconds (legacy field).
	ElapsedMs int `json:"elapsed_ms"`

	// ProcessingTimeMs is the server-side processing time in milliseconds (#1252).
	// Populated from processing_time_ms when present; falls back to ElapsedMs.
	ProcessingTimeMs uint64 `json:"processing_time_ms"`

	// RowCount is the number of rows returned. Matches len(Rows); populated by
	// UnmarshalJSON so callers need not call len() unless they want to.
	RowCount int `json:"row_count"`

	// Columns lists the column names in projection order, when the server
	// provides them.
	Columns []string `json:"columns"`
}

// queryResultWire is the raw wire shape used for normalisation.
type queryResultWire struct {
	Rows             json.RawMessage  `json:"rows"`
	Data             []map[string]any `json:"data"`
	QueryID          string           `json:"query_id"`
	ElapsedMs        int              `json:"elapsed_ms"`
	ProcessingTimeMs uint64           `json:"processing_time_ms"`
	RowCount         int              `json:"row_count"`
	Columns          []string         `json:"columns"`
}

// UnmarshalJSON normalises the two wire shapes the server emits for POST /query
// into the canonical QueryResult. When "data" is present as an array it becomes
// Rows; when "rows" is an integer count (and no "data" array is present) Rows
// is set to an empty slice and RowCount captures the count. RowCount is always
// recomputed as len(Rows).
func (r *QueryResult) UnmarshalJSON(b []byte) error {
	var wire queryResultWire
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	r.QueryID = wire.QueryID
	r.ElapsedMs = wire.ElapsedMs
	// Populate ProcessingTimeMs from the new field; fall back to ElapsedMs (#1252).
	if wire.ProcessingTimeMs > 0 {
		r.ProcessingTimeMs = wire.ProcessingTimeMs
	} else {
		r.ProcessingTimeMs = uint64(wire.ElapsedMs) //nolint:gosec // ElapsedMs is non-negative
	}
	r.Columns = wire.Columns
	if r.Columns == nil {
		r.Columns = []string{}
	}

	rows := []map[string]any{}
	if len(wire.Data) > 0 {
		rows = wire.Data
	} else if len(wire.Rows) > 0 && wire.Rows[0] == '[' {
		// rows is an inline array — decode it.
		if err := json.Unmarshal(wire.Rows, &rows); err != nil {
			return err
		}
	}
	// When wire.Rows is an integer (rows-as-count) and no data array is
	// present, rows stays empty — matching the Python reference behaviour.
	r.Rows = rows
	r.RowCount = len(rows)
	return nil
}

// Each calls fn for every row in the result set, in order.
// It is a convenience wrapper over ranging over Rows.
func (r *QueryResult) Each(fn func(row map[string]any)) {
	for _, row := range r.Rows {
		fn(row)
	}
}

// Column returns all values for a given column name, in row order.
// Missing values (the column was not present in a row) are represented as nil.
func (r *QueryResult) Column(name string) []any {
	out := make([]any, len(r.Rows))
	for i, row := range r.Rows {
		out[i] = row[name] // nil if absent
	}
	return out
}

// HealthResponse is the response from GET /health.
type HealthResponse struct {
	// Status is "ok" when the node is healthy.
	Status string `json:"status"`

	// Profile is one of "free", "server", or "cluster".
	Profile string `json:"profile"`

	// NodeID is the identifier configured via the NODE_ID environment variable.
	NodeID string `json:"node_id"`
}

// IsHealthy returns true when Status == "ok".
func (r *HealthResponse) IsHealthy() bool { return r.Status == "ok" }

// StatusResponse is the response from GET /status.
type StatusResponse struct {
	// Profile is one of "free", "server", or "cluster".
	Profile string `json:"profile"`

	// Role is the cluster role: "coordinator", "reader", or "writer".
	// On free/server profiles this is always "coordinator".
	Role string `json:"role"`

	// QueryQuota is the hard per-principal cost-unit cap configured on the node.
	// Queries whose estimated cost exceeds this value are rejected before execution.
	QueryQuota int `json:"query_quota"`
}

// AuditCountResponse is the response from GET /audit/count.
type AuditCountResponse struct {
	// Entries is the number of audit log entries.
	Entries int `json:"entries"`

	// ChainValid reports whether the hash chain across all audit entries is intact.
	// A false value indicates tampering or corruption and must be treated as a
	// security event.
	ChainValid bool `json:"chain_valid"`
}

// IsTampered returns true when the audit chain is broken (potential tampering).
func (r *AuditCountResponse) IsTampered() bool { return !r.ChainValid }

// ClusterNode describes a single node returned by GET /cluster/nodes.
type ClusterNode struct {
	// NodeID is the identifier for this node (NODE_ID env var on that node).
	NodeID string `json:"node_id"`

	// Role is one of "coordinator", "reader", or "writer".
	Role string `json:"role"`

	// URL is the base HTTP URL of the node.
	URL string `json:"url"`
}

// ClusterNodesResponse is the response from GET /cluster/nodes.
type ClusterNodesResponse struct {
	Nodes []ClusterNode `json:"nodes"`
}

// VersionInfo is the response from GET /version.
// Useful for migration checks and capability negotiation.
type VersionInfo struct {
	// Version is the Relata server version (e.g. "1.1.0").
	Version string `json:"version"`

	// Commit is the git commit hash the binary was built from.
	Commit string `json:"commit"`

	// Profile is the deployment profile — free / server / cluster.
	Profile string `json:"profile"`

	// SchemaVersion is the ontology / row-model schema version.
	SchemaVersion string `json:"schema_version"`

	// Features lists compiled-in feature flags, when the server emits them.
	Features []string `json:"features"`
}

// Stats is the response from GET /debug/stats.
//
// The shape mirrors the partner storage-backend contract §9. Fields the server
// does not yet populate decode as zero values. Raw captures the full server
// response so callers can reach fields the typed model does not surface.
type Stats struct {
	// Records is the total content-addressed blob count (partner §2).
	Records int `json:"records"`

	// States is the total live row count across all types (partner §3).
	States int `json:"states"`

	// SnapshotRows is the total rows in incrementally-refreshed MVs (partner §4).
	SnapshotRows int `json:"snapshot_rows"`

	// LogLeaves is the current WAL write_seq (partner §5).
	LogLeaves int `json:"log_leaves"`

	// Tokens is the current dedup-token count (partner §7).
	Tokens int `json:"tokens"`

	// Raw is the full server response for fields the typed model does not surface.
	Raw map[string]any `json:"-"`
}

// UnmarshalJSON captures the full decoded object into Raw before applying the
// typed fields, so callers always have access to every server-supplied field.
func (s *Stats) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	s.Raw = raw
	s.Records = intFromAny(raw["records"])
	s.States = intFromAny(raw["states"])
	s.SnapshotRows = intFromAny(raw["snapshot_rows"])
	s.LogLeaves = intFromAny(raw["log_leaves"])
	s.Tokens = intFromAny(raw["tokens"])
	return nil
}

// ReadyReport is the response from GET /health/ready.
//
// On HTTP 200 the node is ready to serve. On HTTP 503 the server is shedding
// load and the SDK surfaces a *RelataError wrapping ErrServerError; callers
// that want the typed model even on 503 should inspect the error.
type ReadyReport struct {
	// IsReady is true when the node is ready to serve. Derived from the status
	// string when the server omits the field.
	IsReady bool `json:"is_ready"`

	// Status is the server-side status string (e.g. "ok", "shedding").
	Status string `json:"status"`

	// Reason is the machine-friendly shed tag identifying which condition tripped.
	Reason string `json:"reason"`

	// Detail is an optional human-friendly explanation.
	Detail string `json:"detail"`
}

// UnmarshalJSON derives IsReady from the status string when the server omits it.
func (r *ReadyReport) UnmarshalJSON(b []byte) error {
	type readyAlias ReadyReport
	var alias readyAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}
	*r = ReadyReport(alias)
	if r.Status == "ok" && !r.isReadySet(b) {
		r.IsReady = true
	}
	return nil
}

// isReadySet reports whether the "is_ready" key was present in the raw object.
func (r *ReadyReport) isReadySet(b []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	_, ok := probe["is_ready"]
	return ok
}

// IngestDocumentRequest is the body sent to POST /ingest/document.
type IngestDocumentRequest struct {
	// ChunksJSONL is newline-delimited JSON — one chunk per line.
	ChunksJSONL string `json:"chunks_jsonl"`

	// ManifestJSON is the extraction manifest JSON string.
	ManifestJSON string `json:"manifest_json"`
}

// IngestDocumentResponse is the response from POST /ingest/document.
type IngestDocumentResponse struct {
	// ReportID is the server-assigned manifest ID for the ingested document.
	ReportID string `json:"report_id"`

	// ChunksIngested is the number of chunks accepted into the ingest queue.
	ChunksIngested int `json:"chunks_ingested"`

	// Warnings lists non-fatal protocol warnings (e.g. newer-minor-version fields).
	Warnings []string `json:"warnings"`

	// SchemaVersion is the protocol version the server parsed the document as.
	SchemaVersion string `json:"schema_version"`

	// QueueDepth is the ingest queue depth after this submission.
	QueueDepth int `json:"queue_depth"`
}

// ClientOptions configures the Relata HTTP client.
// All fields are optional; zero values use safe defaults.
type ClientOptions struct {
	// BearerToken is sent as "Authorization: Bearer <token>" on every request.
	// Leave empty for unauthenticated deployments (RELATA_BEARER_TOKEN unset on
	// the server).
	BearerToken string

	// DefaultPurpose is used by Query when no WithPurpose option is provided.
	// Recommended value: the principal use-case for your application (e.g.
	// "investigation", "audit", "analysis"). Must be registered in the tenant's
	// PurposeRegistry.
	DefaultPurpose string

	// Timeout is the end-to-end HTTP request timeout applied to every call.
	// Defaults to 30 seconds if zero.
	Timeout time.Duration

	// HTTPClient overrides the underlying *http.Client. Useful for injecting
	// custom transports (mutual TLS, proxies, tracing). If nil, a new client
	// with Timeout applied is constructed.
	HTTPClient *http.Client

	// Tenant is sent as "X-Relata-Tenant-Id" on every request. Required for
	// multi-tenant deployments.
	Tenant string

	// ActingAs is sent as "X-Acting-As" — the caller asserts membership and the
	// server's wire_acting_as() parses it. Pairs with DelegatedBy.
	ActingAs string

	// DelegatedBy is sent as "X-Delegated-By" — the delegation chain root.
	DelegatedBy string

	// Headers is an optional bag of arbitrary HTTP headers overlaid on every
	// request (e.g. {"X-Request-ID": "..."} for correlation). Caller-supplied
	// headers win over the SDK defaults.
	Headers map[string]string

	// MaxRetries is the number of times to retry a request that fails with HTTP
	// 502/503/504 or a network error. Zero (the default) disables retry.
	MaxRetries int

	// RetryBackoff is the base delay for the exponential retry backoff. The
	// delay for attempt N is RetryBackoff * 2^N. Defaults to 500ms if zero.
	RetryBackoff time.Duration

	// AdminBaseURL points /admin/* and /platform/* requests at the loopbound
	// admin control-plane listener (RELATA_ADMIN_BIND, default
	// 127.0.0.1:9091 — ADR-0261) instead of BaseURL (relatadb/RelataDB#2321).
	// Per ADR-0261 v1, those routes are mounted ONLY on that listener, never
	// the main data-plane listener BaseURL usually targets, so a hardened
	// server/cluster deployment needs this set for BackupClient/
	// TenantAdminClient's platform-tenant methods to reach them at all.
	// Leave empty (the default) when the admin listener isn't split from the
	// data plane (e.g. local/free-profile dev) — every request then goes to
	// BaseURL exactly as before this option existed.
	AdminBaseURL string

	// AllowCleartextBearer explicitly opts in to sending the bearer token
	// over cleartext transports (#3217): an http:// base URL targeting a
	// non-loopback host, or a plaintext grpc:// Arrow Flight endpoint. By
	// default both are refused with ErrCleartextBearerDisallowed so the token
	// can never leak on the wire by accident. Loopback http:// targets and
	// https:// / grpcs:// / tls:// transports are always allowed.
	AllowCleartextBearer bool

	// MaxResponseBytes caps how much of a (non-streaming) response body the
	// client will buffer (#3214). A body larger than this returns
	// ErrResponseTooLarge instead of being read into memory, so a malicious or
	// buggy server cannot OOM the client. Zero (the default) applies the
	// built-in 64 MiB cap. Streaming surfaces (QueryRows / QueryArrowRaw /
	// GetObjectStream) are not capped — they hand the caller an io.ReadCloser.
	MaxResponseBytes int64
}

// MemoryOptions configures the standalone Memory client.
// All fields except purpose (passed to NewMemory) are optional.
type MemoryOptions struct {
	// BearerToken is sent as "Authorization: Bearer <token>" on every request.
	BearerToken string

	// SessionID is the default session id applied to Add/Search when not
	// overridden per call.
	SessionID string

	// Timeout is the per-request HTTP timeout. Defaults to 30 seconds if zero.
	Timeout time.Duration

	// HTTPClient overrides the underlying *http.Client.
	HTTPClient *http.Client

	// Headers is an optional bag of arbitrary HTTP headers overlaid on every
	// request.
	Headers map[string]string

	// Tenant is sent as "X-Relata-Tenant-Id" on every memory request.
	Tenant string
}

// intFromAny coerces a JSON-decoded number (float64) into an int, returning 0
// for nil or non-numeric values. JSON numbers always decode to float64.
func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Search (#670)
// ---------------------------------------------------------------------------

// SearchHit is a single document returned by Search.
type SearchHit struct {
	// ID is the object's unique identifier.
	ID string `json:"id"`
	// ObjectType is the ontology type name.
	ObjectType string `json:"object_type"`
	// Fields holds the object's attribute values.
	Fields map[string]any `json:"fields"`
	// Score is the BM25 relevance score.
	Score float64 `json:"score"`
	// Highlights contains field-level snippets with <em> tags (non-nil when
	// WithHighlight option is used).
	Highlights map[string]string `json:"highlights"`
}

// SearchResponse is the response from the Search method.
type SearchResponse struct {
	// Hits contains the matching documents, sorted by descending score.
	Hits []SearchHit `json:"hits"`
	// Total is the total number of matching documents (may exceed len(Hits)
	// when a limit option is applied).
	Total int `json:"total"`
	// EstimatedTotalHits is the full matching-set size (#967).
	EstimatedTotalHits int `json:"estimatedTotalHits"`
	// Facets contains aggregated counts keyed by field then value.
	Facets map[string]map[string]int `json:"facets"`
	// FacetStats contains numeric facet stats (min/max/sum/avg) (#967).
	FacetStats map[string]map[string]float64 `json:"facetStats"`
	// ProcessingTimeMs is the server-side processing time in milliseconds.
	ProcessingTimeMs int `json:"processing_time_ms"`
}

// SearchOption is a functional option for the Search method.
type SearchOption func(*searchParams)

type searchParams struct {
	limit            int
	facets           []string
	highlight        bool
	filters          map[string]string
	matchingStrategy string
	typoTolerance    map[string]any
	metric           string
	weights          *[3]float64
}

// WithSearchLimit sets the maximum number of hits to return.
func WithSearchLimit(n int) SearchOption {
	return func(p *searchParams) { p.limit = n }
}

// WithSearchFacets requests facet counts for the given fields.
func WithSearchFacets(fields ...string) SearchOption {
	return func(p *searchParams) { p.facets = fields }
}

// WithHighlight enables field-level <em> highlight snippets.
func WithHighlight() SearchOption {
	return func(p *searchParams) { p.highlight = true }
}

// WithSearchFilter adds an equality filter for a field.
func WithSearchFilter(field, value string) SearchOption {
	return func(p *searchParams) {
		if p.filters == nil {
			p.filters = make(map[string]string)
		}
		p.filters[field] = value
	}
}

// WithMatchingStrategy sets the query matching strategy: "all" (AND), "last",
// "frequency", "boolean", or "any" (OR, default) (#967). "boolean" (#3263)
// interprets uppercase AND/OR/NOT operators in the query text as posting-list
// set operations (left-associative; a bare space means OR).
func WithMatchingStrategy(strategy string) SearchOption {
	return func(p *searchParams) { p.matchingStrategy = strategy }
}

// WithTypoTolerance sets per-query typo tolerance config (#967).
// Keys: "enabled" (bool), "min_word_size" (int), "disable_on_words" ([]string),
// "disable_on_attributes" ([]string).
func WithTypoTolerance(config map[string]any) SearchOption {
	return func(p *searchParams) { p.typoTolerance = config }
}

// WithMetric sets the vector distance metric for the HYBRID_SEARCH channel
// (e.g. "cosine", "euclidean", "dot"). Search is BM25-only by default;
// setting this (or WithWeights) is what actually routes the request through
// the server's real HYBRID_SEARCH fusion instead of plain BM25 (#2672).
func WithMetric(metric string) SearchOption {
	return func(p *searchParams) { p.metric = metric }
}

// WithWeights sets the [graph, bm25, vector] fusion weights for HYBRID_SEARCH.
// Search is BM25-only by default; setting this (or WithMetric) is what
// actually routes the request through the server's real HYBRID_SEARCH fusion
// instead of plain BM25 (#2672).
func WithWeights(graph, bm25, vector float64) SearchOption {
	return func(p *searchParams) { p.weights = &[3]float64{graph, bm25, vector} }
}
