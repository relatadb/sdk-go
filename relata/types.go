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

	// RowCount is the number of rows returned. Matches len(Rows).
	RowCount int `json:"row_count"`
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
}
