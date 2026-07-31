package relata

import (
	"context"
)

// McpClient is the synchronous Model Context Protocol client. It wraps
// /mcp/initialize, /mcp/tools, /mcp/tools/call, and provides convenience
// wrappers for the 22 most-used MCP tools. Responses are returned unwrapped
// from the MCP envelope.
type McpClient struct {
	c *Client
}

// NewMcpClient constructs an McpClient that inherits the parent client's auth
// and tenant context. Mirrors the Python reference's McpClient.from_client.
func NewMcpClient(c *Client) *McpClient {
	return &McpClient{c: c}
}

// McpInitializeOptions configures the MCP initialize handshake.
type McpInitializeOptions struct {
	// ClientID is the client identifier advertised to the server.
	ClientID string
	// Version is the protocol version advertised.
	Version string
}

// GetEntitiesOptions configures the GetEntities pagination/filter.
type GetEntitiesOptions struct {
	// FilterExpr is an optional server-side filter expression.
	FilterExpr string
	// Limit caps the response. Defaults to 50.
	Limit int
}

// SearchEntitiesOptions configures SearchEntities.
type SearchEntitiesOptions struct {
	// ObjectType restricts the search to one type.
	ObjectType string
}

// GetDomainSummaryOptions configures GetDomainSummary.
type GetDomainSummaryOptions struct {
	// ObjectType restricts the summary to one type.
	ObjectType string
}

// FindInSocialCorpusOptions configures FindInSocialCorpus.
type FindInSocialCorpusOptions struct {
	// Corpus selects an ingest corpus.
	Corpus string
}

// LookupIdentityOptions configures LookupIdentity.
type LookupIdentityOptions struct {
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// GetTimelineOptions configures GetTimeline.
type GetTimelineOptions struct {
	// SinceNS filters to events at/after the given nanosecond timestamp.
	SinceNS int64
	// UntilNS filters to events at/before the given nanosecond timestamp.
	UntilNS int64
}

// FindConnectionsOptions configures FindConnections.
type FindConnectionsOptions struct {
	// Limit caps the response (default 50, max 200).
	Limit int
}

// GetRelationshipsOptions configures GetRelationships.
type GetRelationshipsOptions struct {
	// Depth is the graph traversal depth (default 1).
	Depth int
}

// AddCaseNoteOptions configures AddCaseNote.
type AddCaseNoteOptions struct {
	// Author is the optional note author.
	Author string
}

// GetAuditTrailOptions configures GetAuditTrail.
type GetAuditTrailOptions struct {
	// CaseID filters to one case.
	CaseID string
	// EntityID filters to one entity.
	EntityID string
}

// RagStoreAnswerOptions configures RagStoreAnswer.
type RagStoreAnswerOptions struct {
	// SourceIDs attaches source memory/object ids to the stored answer.
	SourceIDs []string
}

// RememberOptions configures the MCP Remember tool.
type RememberOptions struct {
	// Confidence clamps to [0, 1]; defaults to 1.0.
	Confidence float64
	// MemoryClass is episodic / semantic / procedural.
	MemoryClass string
}

// RecallOptions configures the MCP Recall tool.
type RecallOptions struct {
	// TopK caps the response. Defaults to 5.
	TopK int
}

// Initialize sends the MCP initialize handshake.
func (m *McpClient) Initialize(ctx context.Context, opts *McpInitializeOptions) (map[string]any, error) {
	clientID := "relata-go-sdk"
	version := "1.0"
	if opts != nil {
		if opts.ClientID != "" {
			clientID = opts.ClientID
		}
		if opts.Version != "" {
			version = opts.Version
		}
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/mcp/initialize", map[string]any{"client_id": clientID, "version": version}, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// ListTools lists every MCP tool the server exposes (30+).
func (m *McpClient) ListTools(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := m.c.get(ctx, "/mcp/tools", &resp); err != nil {
		return nil, err
	}
	inner := unwrapMCP(resp)
	return unwrapList(inner, "tools"), nil
}

// CallTool invokes an MCP tool by name with a typed arguments map.
func (m *McpClient) CallTool(ctx context.Context, name string, arguments map[string]any) (map[string]any, error) {
	payload := map[string]any{"name": name}
	if arguments != nil {
		payload["arguments"] = arguments
	}
	var resp map[string]any
	if err := m.c.postJSON(ctx, "/mcp/tools/call", payload, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// QueryKnowledge is the "query_knowledge" MCP tool — governed SQL query.
func (m *McpClient) QueryKnowledge(ctx context.Context, sql, purpose string) (map[string]any, error) {
	return m.CallTool(ctx, "query_knowledge", map[string]any{"sql": sql, "purpose": purpose})
}

// SearchKnowledge is the "search_knowledge" MCP tool — hybrid BM25 + vector.
func (m *McpClient) SearchKnowledge(ctx context.Context, query, purpose string, topK int) (map[string]any, error) {
	if topK <= 0 {
		topK = 10
	}
	return m.CallTool(ctx, "search_knowledge", map[string]any{
		"query":   query,
		"purpose": purpose,
		"limit":   topK,
	})
}

// ExplainPolicy shows the ACL / org-isolation policy that would apply to sql
// without executing it.
func (m *McpClient) ExplainPolicy(ctx context.Context, sql, purpose string) (map[string]any, error) {
	return m.CallTool(ctx, "explain_policy", map[string]any{"sql": sql, "purpose": purpose})
}

// SuggestExtensions is type/canonical-kind autocomplete.
func (m *McpClient) SuggestExtensions(ctx context.Context, prefix string) (map[string]any, error) {
	_ = prefix
	return m.CallTool(ctx, "suggest_extensions", map[string]any{})
}

// ListEntityTypes returns every registered ontology type.
func (m *McpClient) ListEntityTypes(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "list_entity_types", map[string]any{})
}

// GetEntities is the paginated "get_entities" MCP tool.
func (m *McpClient) GetEntities(ctx context.Context, objectType string, opts *GetEntitiesOptions) (map[string]any, error) {
	limit := 50
	args := map[string]any{"entity_type": objectType, "limit": limit}
	if opts != nil {
		if opts.Limit > 0 {
			args["limit"] = opts.Limit
		}
		if opts.FilterExpr != "" {
			args["filters"] = opts.FilterExpr
		}
	}
	return m.CallTool(ctx, "get_entities", args)
}

// SearchEntities is the free-text "search_entities" MCP tool.
func (m *McpClient) SearchEntities(ctx context.Context, query string, opts *SearchEntitiesOptions) (map[string]any, error) {
	args := map[string]any{"query": query}
	if opts != nil && opts.ObjectType != "" {
		args["entity_types"] = []string{opts.ObjectType}
	}
	return m.CallTool(ctx, "search_entities", args)
}

// GetDomainSummary returns counts + freshness per type.
func (m *McpClient) GetDomainSummary(ctx context.Context, domain string) (map[string]any, error) {
	return m.CallTool(ctx, "get_domain_summary", map[string]any{"domain": domain})
}

// FindInSocialCorpus searches the ingested social-media corpus.
func (m *McpClient) FindInSocialCorpus(ctx context.Context, query string, opts *FindInSocialCorpusOptions) (map[string]any, error) {
	args := map[string]any{"object_type": query}
	return m.CallTool(ctx, "find_in_social_corpus", args)
}

// LookupIdentity is the universal "lookup_identity" MCP tool.
func (m *McpClient) LookupIdentity(ctx context.Context, value string, opts *LookupIdentityOptions) (map[string]any, error) {
	purpose := "analytics"
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	return m.CallTool(ctx, "lookup_identity", map[string]any{"raw": value, "purpose": purpose})
}

// GetEntityProfile returns a rich per-entity dossier.
func (m *McpClient) GetEntityProfile(ctx context.Context, entityID, purpose string) (map[string]any, error) {
	return m.CallTool(ctx, "get_entity_profile", map[string]any{"name": entityID, "purpose": purpose})
}

// GetTimeline returns a chronological event list for an entity.
func (m *McpClient) GetTimeline(ctx context.Context, entityID, purpose string, opts *GetTimelineOptions) (map[string]any, error) {
	_ = opts
	args := map[string]any{"entity": entityID, "purpose": purpose}
	return m.CallTool(ctx, "get_timeline", args)
}

// FindConnections surfaces entities connected to a target via relationships or
// shared attributes.
func (m *McpClient) FindConnections(ctx context.Context, entity, purpose string, opts *FindConnectionsOptions) (map[string]any, error) {
	limit := 50
	args := map[string]any{"entity": entity, "limit": limit, "purpose": purpose}
	if opts != nil && opts.Limit > 0 {
		args["limit"] = opts.Limit
	}
	return m.CallTool(ctx, "find_connections", args)
}

// GetRelationships returns the direct neighbours of an entity.
func (m *McpClient) GetRelationships(ctx context.Context, entityID, purpose string, opts *GetRelationshipsOptions) (map[string]any, error) {
	_ = opts
	args := map[string]any{"subject": entityID, "purpose": purpose}
	return m.CallTool(ctx, "get_relationships", args)
}

// AddCaseNote appends an investigative note to a case.
func (m *McpClient) AddCaseNote(ctx context.Context, caseID, note string, opts *AddCaseNoteOptions) (map[string]any, error) {
	args := map[string]any{"case_id": caseID, "note": note}
	if opts != nil && opts.Author != "" {
		args["author"] = opts.Author
	}
	return m.CallTool(ctx, "add_case_note", args)
}

// GetAuditTrail returns the provenance chain for a case or entity.
func (m *McpClient) GetAuditTrail(ctx context.Context, opts *GetAuditTrailOptions) (map[string]any, error) {
	_ = opts
	args := map[string]any{}
	return m.CallTool(ctx, "get_audit_trail", args)
}

// GetCaseSummary returns an LLM-generated narrative summary of a case.
func (m *McpClient) GetCaseSummary(ctx context.Context, caseID, purpose string) (map[string]any, error) {
	return m.CallTool(ctx, "get_case_summary", map[string]any{"case_id": caseID, "purpose": purpose})
}

// RagStoreAnswer persists a Q&A pair for downstream RAG.
func (m *McpClient) RagStoreAnswer(ctx context.Context, question, answer, purpose string, opts *RagStoreAnswerOptions) (map[string]any, error) {
	args := map[string]any{"question": question, "answer": answer, "purpose": purpose}
	if opts != nil && len(opts.SourceIDs) > 0 {
		args["sources"] = opts.SourceIDs
	}
	return m.CallTool(ctx, "rag_store_answer", args)
}

// RagStoreElements bulk-persists structured RAG elements.
func (m *McpClient) RagStoreElements(ctx context.Context, elements []map[string]any, sourceFilename, purpose string) (map[string]any, error) {
	return m.CallTool(ctx, "rag_store_elements", map[string]any{"elements": elements, "source_filename": sourceFilename, "purpose": purpose})
}

// IngestDocument is the datagrep-envelope document ingest via MCP.
func (m *McpClient) IngestDocument(ctx context.Context, chunksJSONL, manifestJSON, purpose string) (map[string]any, error) {
	return m.CallTool(ctx, "ingest_document", map[string]any{
		"text":    chunksJSONL,
		"source":  manifestJSON,
		"purpose": purpose,
	})
}

// Remember is the "remember" MCP tool — store a memory (same shape as Memory.Add).
func (m *McpClient) Remember(ctx context.Context, content, purpose string, opts *RememberOptions) (map[string]any, error) {
	confidence := 1.0
	memoryClass := "semantic"
	if opts != nil {
		if opts.Confidence != 0 {
			confidence = opts.Confidence
		}
		if opts.MemoryClass != "" {
			memoryClass = opts.MemoryClass
		}
	}
	return m.CallTool(ctx, "remember", map[string]any{
		"content":      content,
		"purpose":      purpose,
		"confidence":   confidence,
		"memory_class": memoryClass,
	})
}

// Recall is the "recall" MCP tool.
func (m *McpClient) Recall(ctx context.Context, query, purpose string, opts *RecallOptions) (map[string]any, error) {
	topK := 5
	if opts != nil && opts.TopK > 0 {
		topK = opts.TopK
	}
	return m.CallTool(ctx, "recall", map[string]any{"query": query, "purpose": purpose, "top_k": topK})
}
