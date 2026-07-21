package relata

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultTimeout      = 30 * time.Second
	defaultContentType  = "application/json"
	defaultRetryBackoff = 500 * time.Millisecond

	headerAuthorization = "Authorization"
	headerContentType   = "Content-Type"
	headerAccept        = "Accept"
	headerRequestID     = "X-Request-ID"
	headerTenant        = "X-Organization-Id"
	headerActingAs      = "X-Acting-As"
	headerDelegatedBy   = "X-Delegated-By"
	headerRetryAfter    = "Retry-After"

	userAgent = "relata-go/0.2.0"

	// defaultRetryMaxAttempts caps the retry loop even when MaxRetries is large.
	defaultRetryMaxAttempts = 5
)

// Client is a thread-safe HTTP client for the Relata data engine.
// A single Client may be shared across goroutines.
type Client struct {
	baseURL        string
	bearerToken    string
	defaultPurpose string
	tenant         string
	actingAs       string
	delegatedBy    string
	headers        map[string]string
	http           *http.Client
	maxRetries     int
	retryBackoff   time.Duration
}

// New constructs a Client that targets baseURL. opts may be nil, in which case
// all defaults apply (30 s timeout, no authentication, no default purpose, no
// retry).
//
// baseURL must not have a trailing slash, e.g. "http://localhost:9090".
func New(baseURL string, opts *ClientOptions) *Client {
	baseURL = strings.TrimRight(baseURL, "/")

	c := &Client{
		baseURL:      baseURL,
		retryBackoff: defaultRetryBackoff,
	}
	if opts == nil {
		c.http = &http.Client{Timeout: defaultTimeout}
		return c
	}

	c.bearerToken = opts.BearerToken
	c.defaultPurpose = opts.DefaultPurpose
	c.tenant = opts.Tenant
	c.actingAs = opts.ActingAs
	c.delegatedBy = opts.DelegatedBy
	c.maxRetries = opts.MaxRetries
	if opts.RetryBackoff > 0 {
		c.retryBackoff = opts.RetryBackoff
	}
	if len(opts.Headers) > 0 {
		c.headers = make(map[string]string, len(opts.Headers))
		for k, v := range opts.Headers {
			c.headers[k] = v
		}
	}

	if opts.HTTPClient != nil {
		c.http = opts.HTTPClient
	} else {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = defaultTimeout
		}
		c.http = &http.Client{Timeout: timeout}
	}

	return c
}

// Query executes sql against the Relata engine and returns the result set.
//
// A purpose token must be supplied either via the DefaultPurpose field on
// ClientOptions or the WithPurpose QueryOption. If neither is present,
// ErrPurposeRequired is returned before any network call is made.
//
// Example:
//
//	result, err := client.Query(ctx,
//	    "SELECT * FROM Person AS OF '2025-01-01' LIMIT 20",
//	    relata.WithPurpose("investigation"),
//	)
func (c *Client) Query(ctx context.Context, sql string, opts ...QueryOption) (*QueryResult, error) {
	cfg := &queryConfig{
		purpose: c.defaultPurpose,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.purpose == "" {
		return nil, ErrPurposeRequired
	}

	payload := QueryRequest{
		Purpose: cfg.purpose,
		SQL:     sql,
	}

	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}

	var result QueryResult
	if err := c.postJSON(ctx, "/query", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Search calls POST /search and returns matching documents for the given query
// and object type (#670). Optional functional options control limit, facets,
// highlight, and per-field filters.
//
//	results, err := client.Search(ctx, "alice smith", "Person",
//	    WithSearchLimit(10), WithHighlight(), WithSearchFacets("tenant_id"))
func (c *Client) Search(ctx context.Context, query, objectType string, opts ...SearchOption) (*SearchResponse, error) {
	p := &searchParams{}
	for _, o := range opts {
		o(p)
	}
	body := map[string]any{
		"query":     query,
		"type":      objectType,
		"highlight": p.highlight,
	}
	if p.limit > 0 {
		body["limit"] = p.limit
	}
	if len(p.facets) > 0 {
		body["facets"] = p.facets
	}
	if len(p.filters) > 0 {
		body["filters"] = p.filters
	}
	if p.matchingStrategy != "" {
		body["matching_strategy"] = p.matchingStrategy
	}
	if p.typoTolerance != nil {
		body["typo_tolerance"] = p.typoTolerance
	}
	var resp SearchResponse
	if err := c.postJSON(ctx, "/search", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// MultiSearch runs N federated queries in one round-trip (#967).
// Each query is a map with "query", "type", and optional "limit".
func (c *Client) MultiSearch(ctx context.Context, queries []map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/multi-search", map[string]any{"queries": queries}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ── Type management & ontology (#967) ───────────────────────────────────────

// ListTypes lists all registered object types with row counts.
func (c *Client) ListTypes(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := c.get(ctx, "/types", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RegisterType registers a custom object type at runtime.
func (c *Client) RegisterType(ctx context.Context, name string, spec map[string]any) (map[string]any, error) {
	body := map[string]any{"name": name}
	for k, v := range spec {
		body[k] = v
	}
	var resp map[string]any
	if err := c.postJSON(ctx, "/types", body, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DeregisterType deregisters a custom type. Admin token required.
func (c *Client) DeregisterType(ctx context.Context, name string) (map[string]any, error) {
	var resp map[string]any
	if err := c.delete(ctx, "/types/"+name, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// TypeDetail gets type detail (properties, owner, row count).
func (c *Client) TypeDetail(ctx context.Context, name string) (map[string]any, error) {
	var resp map[string]any
	if err := c.get(ctx, "/types/"+name, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// OntologyMigrate applies a SHACL schema migration.
func (c *Client) OntologyMigrate(ctx context.Context, schema map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/ontology/migrate", schema, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// EnrichmentRules registers identity enrichment rules for SmartIngest.
func (c *Client) EnrichmentRules(ctx context.Context, rules map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/ontology/enrichment-rules", rules, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListModules lists installed modules / extensions.
func (c *Client) ListModules(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := c.get(ctx, "/modules", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// CreateLink creates a typed governed link (edge) between two objects.
func (c *Client) CreateLink(ctx context.Context, params map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/links", params, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// UpsertTyped inserts a typed object (any struct). The struct is JSON-marshalled
// and sent to /ingest — the Go compiler ensures field-name correctness at build
// time (#967 Tier 2a).
//
//	type Person struct {
//	    PK   string `json:"_pk"`
//	    Name string `json:"name"`
//	}
//	client.UpsertTyped(ctx, "Person", "p1", Person{PK: "p1", Name: "Alice"})
func (c *Client) UpsertTyped(ctx context.Context, objectType, pk string, obj any) (map[string]any, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("relata: marshal typed object: %w", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("relata: unmarshal typed object: %w", err)
	}
	fields["_pk"] = pk
	ndjson, _ := json.Marshal(fields)
	status, respBody, _, err := c.rawHTTPRequest(ctx, "POST",
		encodeGetURL("/ingest", map[string]string{"object_type": objectType, "purpose": "typed-crud"}),
		ndjson, "application/x-ndjson")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, errorFromStatus(status, respBody, "", 0)
	}
	var resp map[string]any
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("relata: decode response: %w", err)
		}
	}
	return resp, nil
}

// ── Identity resolution & entity lifecycle (#967) ───────────────────────────

// ResolveIdentity resolves an identity value to all known objects/clusters.
func (c *Client) ResolveIdentity(ctx context.Context, purpose, value string) (map[string]any, error) {
	sql := fmt.Sprintf("RESOLVE_IDENTITY('%s')", strings.ReplaceAll(value, "'", "''"))
	return c.query(ctx, purpose, sql)
}

// DetectIdentities detects identities in free text via SmartIngest.
func (c *Client) DetectIdentities(ctx context.Context, purpose, text string) (map[string]any, error) {
	sql := fmt.Sprintf("DETECT_IDENTITIES('%s')", strings.ReplaceAll(text, "'", "''"))
	return c.query(ctx, purpose, sql)
}

// EraseSubject performs GDPR Art. 17 erasure — irreversible.
func (c *Client) EraseSubject(ctx context.Context, purpose, subject, reason string) (map[string]any, error) {
	s := strings.ReplaceAll(subject, "'", "''")
	r := strings.ReplaceAll(reason, "'", "''")
	if reason == "" {
		r = "gdpr-art17-request"
	}
	sql := fmt.Sprintf("ERASE SUBJECT '%s' REASON '%s' CERTIFY", s, r)
	return c.query(ctx, purpose, sql)
}

// ── SPARQL, sessions & cluster (#967 Tier 2d) ───────────────────────────────

// ExportData bulk-exports all rows of a type (#967 Tier 5c).
func (c *Client) ExportData(ctx context.Context, objectType, format string) (map[string]any, error) {
	if format == "" {
		format = "json"
	}
	var resp map[string]any
	if err := c.get(ctx, fmt.Sprintf("/export?type=%s&format=%s&purpose=export", objectType, format), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RegisterWebhook registers a webhook for push notifications (#967 Tier 5b).
func (c *Client) RegisterWebhook(ctx context.Context, url string, eventTypes []string) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/webhooks", map[string]any{"url": url, "event_types": eventTypes}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListWebhooks lists registered webhooks.
func (c *Client) ListWebhooks(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := c.get(ctx, "/webhooks", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DeleteWebhook deletes a webhook.
func (c *Client) DeleteWebhook(ctx context.Context, id string) (map[string]any, error) {
	var resp map[string]any
	if err := c.delete(ctx, "/webhooks/"+id, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Sparql executes a SPARQL query.
func (c *Client) Sparql(ctx context.Context, query string) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/sparql", map[string]any{"query": query}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ClusterTopology returns the cluster topology.
func (c *Client) ClusterTopology(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := c.get(ctx, "/cluster/topology", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ClusterRebalance triggers a cluster rebalance.
func (c *Client) ClusterRebalance(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/cluster/rebalance", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ClusterDrain drains a node for maintenance.
func (c *Client) ClusterDrain(ctx context.Context, nodeID string) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/cluster/drain/"+nodeID, map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SessionDiff views uncommitted session changes.
func (c *Client) SessionDiff(ctx context.Context, sessionID string) (map[string]any, error) {
	var resp map[string]any
	if err := c.get(ctx, "/session/"+sessionID+"/diff", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SessionCommit commits a session's draft writes.
func (c *Client) SessionCommit(ctx context.Context, sessionID string) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/session/"+sessionID+"/commit", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SessionDiscard discards uncommitted session changes.
func (c *Client) SessionDiscard(ctx context.Context, sessionID string) (map[string]any, error) {
	var resp map[string]any
	if err := c.delete(ctx, "/session/"+sessionID+"/draft", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ── Entity merge, dedup & identity (#967) ───────────────────────────────────

// IdentityCluster resolves an identity to its full cluster of linked identifiers.
func (c *Client) IdentityCluster(ctx context.Context, purpose, value string) (map[string]any, error) {
	sql := fmt.Sprintf("RESOLVE_IDENTITY('%s', MODE => 'cluster')", strings.ReplaceAll(value, "'", "''"))
	return c.query(ctx, purpose, sql)
}

// FuseIdentities merges two identities — writes an IdentityLink with
// link_type='fused' and returns the merged cluster (#967).
func (c *Client) FuseIdentities(ctx context.Context, purpose, idA, idB string) (map[string]any, error) {
	sql := fmt.Sprintf("FUSE_IDENTITIES('%s', '%s')", strings.ReplaceAll(idA, "'", "''"), strings.ReplaceAll(idB, "'", "''"))
	return c.query(ctx, purpose, sql)
}

// SplitIdentities unmerges two identities — inverse of FuseIdentities (#967).
func (c *Client) SplitIdentities(ctx context.Context, purpose, idA, idB string) (map[string]any, error) {
	sql := fmt.Sprintf("SPLIT_IDENTITIES('%s', '%s')", strings.ReplaceAll(idA, "'", "''"), strings.ReplaceAll(idB, "'", "''"))
	return c.query(ctx, purpose, sql)
}

// ── Graph algorithm operators (#967) ─────────────────────────────────────────

// GraphDijkstra finds the shortest path between two entities.
func (c *Client) GraphDijkstra(ctx context.Context, purpose, objectType, from, to string) (map[string]any, error) {
	sql := fmt.Sprintf("GRAPH_DIJKSTRA('%s', FROM => '%s', TO => '%s')", objectType, from, to)
	return c.query(ctx, purpose, sql)
}

// GraphPageRank computes PageRank centrality.
func (c *Client) GraphPageRank(ctx context.Context, purpose, objectType string, damping float64, maxIter int) (map[string]any, error) {
	sql := fmt.Sprintf("GRAPH_PAGERANK('%s', DAMPING => %f, MAX_ITER => %d)", objectType, damping, maxIter)
	return c.query(ctx, purpose, sql)
}

// GraphSCC finds strongly connected components (fraud-ring detection).
func (c *Client) GraphSCC(ctx context.Context, purpose, objectType string) (map[string]any, error) {
	return c.query(ctx, purpose, fmt.Sprintf("GRAPH_SCC('%s')", objectType))
}

// GraphCycles detects cycles in the graph.
func (c *Client) GraphCycles(ctx context.Context, purpose, objectType string) (map[string]any, error) {
	return c.query(ctx, purpose, fmt.Sprintf("GRAPH_CYCLES('%s')", objectType))
}

// GraphCommunity detects communities via label propagation.
func (c *Client) GraphCommunity(ctx context.Context, purpose, objectType string) (map[string]any, error) {
	return c.query(ctx, purpose, fmt.Sprintf("GRAPH_COMMUNITY('%s')", objectType))
}

// GraphNodeSimilarity finds entities similar to a seed node.
func (c *Client) GraphNodeSimilarity(ctx context.Context, purpose, objectType, node string) (map[string]any, error) {
	sql := fmt.Sprintf("GRAPH_NODE_SIMILARITY('%s', NODE => '%s')", objectType, node)
	return c.query(ctx, purpose, sql)
}

// GraphLinkPredict predicts missing relationships.
func (c *Client) GraphLinkPredict(ctx context.Context, purpose, objectType string) (map[string]any, error) {
	return c.query(ctx, purpose, fmt.Sprintf("GRAPH_LINK_PREDICT('%s')", objectType))
}

// GraphTriangleCount counts triangles (graph density / cohesion).
func (c *Client) GraphTriangleCount(ctx context.Context, purpose, objectType string) (map[string]any, error) {
	return c.query(ctx, purpose, fmt.Sprintf("TRIANGLE_COUNT('%s')", objectType))
}

// ── Intelligence operators (#967) ────────────────────────────────────────────

// BeneficialOwnershipChain traces ownership to ultimate beneficial owner.
func (c *Client) BeneficialOwnershipChain(ctx context.Context, purpose, party string, maxDepth int) (map[string]any, error) {
	sql := fmt.Sprintf("BENEFICIAL_OWNERSHIP_CHAIN('%s', MAX_DEPTH => %d)", party, maxDepth)
	return c.query(ctx, purpose, sql)
}

// SanctionsScreen screens against sanctions lists with fuzzy threshold.
func (c *Client) SanctionsScreen(ctx context.Context, purpose, party string, threshold float64) (map[string]any, error) {
	sql := fmt.Sprintf("SANCTIONS_SCREEN('%s', THRESHOLD => %f)", party, threshold)
	return c.query(ctx, purpose, sql)
}

// ConvoyDetect finds entities traveling together.
func (c *Client) ConvoyDetect(ctx context.Context, purpose string, radiusM float64, timeTolSecs int, minPoints int) (map[string]any, error) {
	sql := fmt.Sprintf("CONVOY(RADIUS => %f, TIME_TOL => %d, MIN_POINTS => %d)", radiusM, timeTolSecs*1_000_000_000, minPoints)
	return c.query(ctx, purpose, sql)
}

// BurnerDetect detects burner phone patterns.
func (c *Client) BurnerDetect(ctx context.Context, purpose string, maxAgeDays int, maxCalls int) (map[string]any, error) {
	sql := fmt.Sprintf("BURNER_DETECT(MAX_AGE => %d, MAX_CALLS => %d)", maxAgeDays*86_400_000_000_000, maxCalls)
	return c.query(ctx, purpose, sql)
}

// CryptoTrace follows cryptocurrency fund flow.
func (c *Client) CryptoTrace(ctx context.Context, purpose, entity string) (map[string]any, error) {
	return c.query(ctx, purpose, fmt.Sprintf("CRYPTO_TRACE('%s')", entity))
}

// DnsTunnelDetect detects DNS tunneling.
func (c *Client) DnsTunnelDetect(ctx context.Context, purpose, entity string) (map[string]any, error) {
	return c.query(ctx, purpose, fmt.Sprintf("DNS_TUNNEL_DETECT('%s')", entity))
}

// CrimePatternCluster performs spatial crime pattern analysis.
func (c *Client) CrimePatternCluster(ctx context.Context, purpose, area string) (map[string]any, error) {
	return c.query(ctx, purpose, fmt.Sprintf("CRIME_PATTERN_CLUSTER('%s')", area))
}

// Geofence finds entities within a geographic fence.
func (c *Client) Geofence(ctx context.Context, purpose, fence, targetType string) (map[string]any, error) {
	sql := fmt.Sprintf("GEOFENCE('%s', TARGET_TYPE => '%s')", fence, targetType)
	return c.query(ctx, purpose, sql)
}

// query is a helper that posts SQL to /query.
func (c *Client) query(ctx context.Context, purpose, sql string) (map[string]any, error) {
	var resp map[string]any
	if err := c.postJSON(ctx, "/query", map[string]any{"purpose": purpose, "sql": sql}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Health calls GET /health and returns the node health status.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.get(ctx, "/health", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Status calls GET /status and returns the node operational status.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.get(ctx, "/status", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AuditCount calls GET /audit/count and returns the audit log summary.
// If the returned ChainValid field is false, this method also returns
// ErrChainCorrupted so that callers who do not inspect the struct still
// receive an actionable error.
func (c *Client) AuditCount(ctx context.Context) (*AuditCountResponse, error) {
	var resp AuditCountResponse
	if err := c.get(ctx, "/audit/count", &resp); err != nil {
		return nil, err
	}
	if !resp.ChainValid {
		return &resp, ErrChainCorrupted
	}
	return &resp, nil
}

// ClusterNodes calls GET /cluster/nodes and returns the list of known nodes.
// On free/server profiles the list contains only the local node.
func (c *Client) ClusterNodes(ctx context.Context) ([]ClusterNode, error) {
	var resp ClusterNodesResponse
	if err := c.get(ctx, "/cluster/nodes", &resp); err != nil {
		return nil, err
	}
	return resp.Nodes, nil
}

// Stats calls GET /debug/stats and returns engine-wide counts for health
// dashboards (records, states, snapshot_rows, log_leaves, tokens).
func (c *Client) Stats(ctx context.Context) (*Stats, error) {
	var resp Stats
	if err := c.get(ctx, "/debug/stats", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Version calls GET /version and returns runtime build-info. Useful for
// migration checks and capability negotiation.
func (c *Client) Version(ctx context.Context) (*VersionInfo, error) {
	var resp VersionInfo
	if err := c.get(ctx, "/version", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Ready calls GET /health/ready and returns the 9-condition readiness report.
// On HTTP 503 the server is shedding load; the returned error wraps
// ErrServerError and carries the shed reason in the Message field.
func (c *Client) Ready(ctx context.Context) (*ReadyReport, error) {
	var resp ReadyReport
	if err := c.get(ctx, "/health/ready", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// IngestDocument submits a datagrep-extractor document (the _chunks.jsonl and
// _manifest.json outputs) to POST /ingest/document. The server parses and
// version-checks the protocol envelope, then queues the chunks for storage.
func (c *Client) IngestDocument(ctx context.Context, chunksJSONL, manifestJSON string) (*IngestDocumentResponse, error) {
	payload := IngestDocumentRequest{
		ChunksJSONL:  chunksJSONL,
		ManifestJSON: manifestJSON,
	}
	var resp IngestDocumentResponse
	if err := c.postJSON(ctx, "/ingest/document", payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ---- internal helpers -------------------------------------------------------
//
// These accessors are unexported but shared with the typed client modules
// (governance.go, mcp.go, …) so every client reuses one transport, one retry
// loop, one X-Request-ID generator, and one RFC 7807 classifier.

// baseURL returns the configured server base URL. Typed clients use it to
// build absolute paths when they need to step outside the JSON helpers.
func (c *Client) baseURLValue() string { return c.baseURL }

// httpClient returns the underlying *http.Client. Typed clients (e.g. the S3
// door wrapper) use it for non-JSON or raw streaming requests.
func (c *Client) httpClient() *http.Client { return c.http }

// bearer returns the configured bearer token. Used by clients that open their
// own requests against the shared http client.
func (c *Client) bearer() string { return c.bearerToken }

// tenantID returns the configured X-Organization-Id, if any.
func (c *Client) tenantID() string { return c.tenant }

// sharedHeaders returns a copy of the static caller-supplied header bag, for
// clients that build their own requests.
func (c *Client) sharedHeaders() map[string]string {
	if len(c.headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.headers))
	for k, v := range c.headers {
		out[k] = v
	}
	return out
}

// get performs a GET request to path and JSON-decodes the response into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.doRequest(ctx, http.MethodGet, path, nil, "", out)
}

// postJSON performs a POST request to path with body JSON-encoded from in, and
// JSON-decodes the response into out.
func (c *Client) postJSON(ctx context.Context, path string, in any, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("relata: marshal request: %w", err)
	}
	return c.doRequest(ctx, http.MethodPost, path, data, defaultContentType, out)
}

// delete performs a DELETE request to path and JSON-decodes the response.
func (c *Client) delete(ctx context.Context, path string, out any) error {
	return c.doRequest(ctx, http.MethodDelete, path, nil, "", out)
}

// patchJSON performs a PATCH request to path with a JSON body.
func (c *Client) patchJSON(ctx context.Context, path string, in any, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("relata: marshal request: %w", err)
	}
	return c.doRequest(ctx, http.MethodPatch, path, data, defaultContentType, out)
}

// putJSON performs a PUT request to path with a JSON body.
func (c *Client) putJSON(ctx context.Context, path string, in any, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("relata: marshal request: %w", err)
	}
	return c.doRequest(ctx, http.MethodPut, path, data, defaultContentType, out)
}

// postRaw performs a POST request to path with a raw (non-JSON) body and the
// given content type. Used by the NDJSON/CSV ingest paths.
func (c *Client) postRaw(ctx context.Context, path, contentType string, body []byte, out any) error {
	return c.doRequest(ctx, http.MethodPost, path, body, contentType, out)
}

// doRequest is the single transport entry point. It builds the request, sets
// the common headers (including a per-request X-Request-ID), executes with
// retry on 502/503/504 + network errors, reads the body, classifies errors via
// RFC 7807 problem+json, and decodes successful responses into out.
func (c *Client) doRequest(ctx context.Context, method, path string, body []byte, contentType string, out any) error {
	url := c.baseURL + path

	maxAttempts := c.maxRetries
	if maxAttempts > defaultRetryMaxAttempts {
		maxAttempts = defaultRetryMaxAttempts
	}

	var lastErr error
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		// Rewindable body reader per attempt.
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return fmt.Errorf("relata: build request: %w", err)
		}
		if contentType != "" {
			req.Header.Set(headerContentType, contentType)
		}
		c.setHeaders(req)

		resp, err := c.http.Do(req)
		if err != nil {
			// Network error or timeout. Retry if attempts remain and the
			// context is still live.
			lastErr = c.classifyTransportError(ctx, err)
			if attempt < maxAttempts && isRetryableTransport(ctx, err) {
				c.sleepBackoff(ctx, attempt)
				continue
			}
			return lastErr
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("relata: read body: %w", readErr)
			if attempt < maxAttempts {
				c.sleepBackoff(ctx, attempt)
				continue
			}
			return lastErr
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out == nil || len(respBody) == 0 {
				return nil
			}
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("relata: decode response: %w", err)
			}
			return nil
		}

		// Error status. Retry 502/503/504 if attempts remain.
		rid := resp.Header.Get(headerRequestID)
		retryAfter := parseRetryAfter(resp.Header.Get(headerRetryAfter))
		rerr := errorFromStatus(resp.StatusCode, respBody, rid, retryAfter)
		if isRetryableStatus(resp.StatusCode) && attempt < maxAttempts {
			lastErr = rerr
			c.sleepBackoffWithJitter(ctx, attempt, retryAfter)
			continue
		}
		return rerr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("relata: retries exhausted")
	}
	return lastErr
}

// setHeaders applies the common headers to every outbound request. A
// per-request X-Request-ID is generated unless the caller pinned one in the
// static headers bag, in which case the caller value wins.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set(headerAccept, defaultContentType)
	req.Header.Set("User-Agent", userAgent)
	if c.bearerToken != "" {
		req.Header.Set(headerAuthorization, "Bearer "+c.bearerToken)
	}
	if c.tenant != "" {
		req.Header.Set(headerTenant, c.tenant)
	}
	if c.actingAs != "" {
		req.Header.Set(headerActingAs, c.actingAs)
	}
	if c.delegatedBy != "" {
		req.Header.Set(headerDelegatedBy, c.delegatedBy)
	}
	// Caller-supplied headers win over the SDK defaults. This includes a
	// caller-pinned X-Request-ID, which we then do not override.
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	// Auto-generate a per-request X-Request-ID when the caller did not pin one.
	if req.Header.Get(headerRequestID) == "" {
		if id, err := newRequestID(); err == nil {
			req.Header.Set(headerRequestID, id)
		}
	}
}

// classifyTransportError maps a net/http / context error to a *RelataError
// wrapping ErrConnection (for unreachable / timed-out requests).
func (c *Client) classifyTransportError(ctx context.Context, err error) *RelataError {
	if ctx.Err() != nil {
		// Context cancellation / deadline — surface as the context error.
		return &RelataError{
			StatusCode: 0,
			Message:    ctx.Err().Error(),
			Err:        ErrConnection,
		}
	}
	return &RelataError{
		StatusCode: 0,
		Message:    err.Error(),
		Err:        ErrConnection,
	}
}

// sleepBackoff sleeps for the exponential backoff (backoff * 2^attempt) unless
// the context is cancelled.
func (c *Client) sleepBackoff(ctx context.Context, attempt int) {
	delay := c.retryBackoff << min(attempt, 6)
	if delay <= 0 {
		delay = c.retryBackoff
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

// sleepBackoffWithJitter sleeps for the exponential backoff, honouring a
// server-supplied Retry-After when it is larger than the computed backoff.
func (c *Client) sleepBackoffWithJitter(ctx context.Context, attempt int, retryAfter time.Duration) {
	delay := c.retryBackoff << min(attempt, 6)
	if delay <= 0 {
		delay = c.retryBackoff
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

// isRetryableStatus reports whether the HTTP status warrants a retry.
func isRetryableStatus(code int) bool {
	return code == 502 || code == 503 || code == 504
}

// isRetryableTransport reports whether a transport-level error is worth
// retrying. Context cancellation is never retried.
func isRetryableTransport(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	return true
}

// parseRetryAfter parses the Retry-After header (HTTP-date or delta-seconds).
// Returns zero when the header is absent or unparseable.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	// Delta-seconds form.
	if d, err := time.ParseDuration(v + "s"); err == nil {
		return d
	}
	// HTTP-date form.
	if t, err := http.ParseTime(v); err == nil {
		if dur := time.Until(t); dur > 0 {
			return dur
		}
	}
	return 0
}

// newRequestID generates a random version-4 UUID string using crypto/rand,
// with no external dependencies.
func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// RFC 4122 v4 / variant 10xx.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
