// Package relata provides a Go client for the Relata data engine.
//
// Relata is an ontology-driven, enterprise-grade data engine. Every query
// MUST declare a purpose token registered in the tenant's PurposeRegistry;
// queries that omit a purpose are rejected at the wire.
//
// Basic usage:
//
//	client := relata.New("http://localhost:8080", &relata.ClientOptions{
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

	// ElapsedMs is the server-side wall-clock time in milliseconds.
	ElapsedMs int `json:"elapsed_ms"`

	// RowCount is the number of rows returned. Matches len(Rows); populated by
	// UnmarshalJSON so callers need not call len() unless they want to.
	RowCount int `json:"row_count"`

	// Columns lists the column names in projection order, when the server
	// provides them.
	Columns []string `json:"columns"`
}

// queryResultWire is the raw wire shape used for normalisation.
type queryResultWire struct {
	Rows      json.RawMessage  `json:"rows"`
	Data      []map[string]any `json:"data"`
	QueryID   string           `json:"query_id"`
	ElapsedMs int              `json:"elapsed_ms"`
	RowCount  int              `json:"row_count"`
	Columns   []string         `json:"columns"`
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

	// Profile is one of "lite", "server", or "cluster".
	Profile string `json:"profile"`

	// NodeID is the identifier configured via the NODE_ID environment variable.
	NodeID string `json:"node_id"`
}

// IsHealthy returns true when Status == "ok".
func (r *HealthResponse) IsHealthy() bool { return r.Status == "ok" }

// StatusResponse is the response from GET /status.
type StatusResponse struct {
	// Profile is one of "lite", "server", or "cluster".
	Profile string `json:"profile"`

	// Role is the cluster role: "coordinator", "reader", or "writer".
	// On lite/server profiles this is always "coordinator".
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

	// Profile is the deployment profile — lite / server / cluster.
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

	// Tenant is sent as "X-Organization-Id" on every request. Required for
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

	// Tenant is sent as "X-Organization-Id" on every memory request.
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
