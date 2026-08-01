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

// ---------------------------------------------------------------------------
// #2322 — 42 (+4) previously Rust-only MCP tool wrappers, ported to Go.
// ---------------------------------------------------------------------------

// RememberBatchOptions configures RememberBatch.
type RememberBatchOptions struct {
	// Purpose is the default purpose token applied to items that omit their own.
	Purpose string
}

// RememberBatch stores many governed memory items in one call ("remember_batch").
func (m *McpClient) RememberBatch(ctx context.Context, items []map[string]any, opts *RememberBatchOptions) (map[string]any, error) {
	args := map[string]any{"items": items}
	if opts != nil && opts.Purpose != "" {
		args["purpose"] = opts.Purpose
	}
	return m.CallTool(ctx, "remember_batch", args)
}

// Recognize looks up a stored memory item by id ("recognize").
func (m *McpClient) Recognize(ctx context.Context, memoryID, purpose string) (map[string]any, error) {
	args := map[string]any{"id": memoryID}
	if purpose != "" {
		args["purpose"] = purpose
	}
	return m.CallTool(ctx, "recognize", args)
}

// EpisodesInOptions configures EpisodesIn.
type EpisodesInOptions struct {
	// Limit caps the response. Defaults to 20.
	Limit int
	// Purpose is the declared purpose token.
	Purpose string
}

// EpisodesIn lists Episodes within an AgentSession ("episodes_in").
func (m *McpClient) EpisodesIn(ctx context.Context, sessionID string, opts *EpisodesInOptions) (map[string]any, error) {
	limit := 20
	args := map[string]any{"session_id": sessionID}
	if opts != nil {
		if opts.Limit > 0 {
			limit = opts.Limit
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	args["limit"] = limit
	return m.CallTool(ctx, "episodes_in", args)
}

// Justify returns the provenance/audit chain for a memory object ("justify").
func (m *McpClient) Justify(ctx context.Context, memoryID, purpose string) (map[string]any, error) {
	args := map[string]any{"id": memoryID}
	if purpose != "" {
		args["purpose"] = purpose
	}
	return m.CallTool(ctx, "justify", args)
}

// ConsolidateOptions configures Consolidate.
type ConsolidateOptions struct {
	// Confidence in the updated belief (0-1). Defaults to 1.0.
	Confidence float64
	// Purpose is the declared purpose token.
	Purpose string
}

// Consolidate supersedes an existing memory item with an updated belief ("consolidate").
func (m *McpClient) Consolidate(ctx context.Context, memoryID, content string, opts *ConsolidateOptions) (map[string]any, error) {
	confidence := 1.0
	args := map[string]any{"id": memoryID, "content": content}
	if opts != nil {
		if opts.Confidence != 0 {
			confidence = opts.Confidence
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	args["confidence"] = confidence
	return m.CallTool(ctx, "consolidate", args)
}

// ForgetOptions configures Forget.
type ForgetOptions struct {
	// RetainDays before deletion (0 = immediate, -1 = legal hold). Defaults to 0.
	RetainDays int
	// Purpose is the declared purpose token.
	Purpose string
}

// Forget applies a retention policy to a memory item ("forget").
func (m *McpClient) Forget(ctx context.Context, memoryID string, opts *ForgetOptions) (map[string]any, error) {
	args := map[string]any{"id": memoryID, "retain_days": 0}
	if opts != nil {
		if opts.RetainDays != 0 {
			args["retain_days"] = opts.RetainDays
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	return m.CallTool(ctx, "forget", args)
}

// RememberProcedure stores a versioned agent procedure (T16/#2233, "remember_procedure").
func (m *McpClient) RememberProcedure(ctx context.Context, agentID, name, instructionText, purpose string) (map[string]any, error) {
	args := map[string]any{"agent_id": agentID, "name": name, "instruction_text": instructionText}
	if purpose != "" {
		args["purpose"] = purpose
	}
	return m.CallTool(ctx, "remember_procedure", args)
}

// RecallProcedureOptions configures RecallProcedure.
type RecallProcedureOptions struct {
	// Name restricts recall to one procedure name.
	Name string
	// AllVersions returns every stored version instead of only the latest per name.
	AllVersions bool
	// Limit caps the response. Defaults to 20.
	Limit int
	// Purpose is the declared purpose token.
	Purpose string
}

// RecallProcedure reads back stored procedures for an agent (T16/#2233, "recall_procedure").
func (m *McpClient) RecallProcedure(ctx context.Context, agentID string, opts *RecallProcedureOptions) (map[string]any, error) {
	limit := 20
	args := map[string]any{"agent_id": agentID}
	if opts != nil {
		if opts.Name != "" {
			args["name"] = opts.Name
		}
		args["all_versions"] = opts.AllVersions
		if opts.Limit > 0 {
			limit = opts.Limit
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	args["limit"] = limit
	return m.CallTool(ctx, "recall_procedure", args)
}

// AssociateOptions configures Associate.
type AssociateOptions struct {
	// Relation label (e.g. caused_by, contradicts). Defaults to "related_to".
	Relation string
	// Purpose is the declared purpose token.
	Purpose string
}

// Associate links two memory items/entities with a typed relation ("associate").
func (m *McpClient) Associate(ctx context.Context, fromID, toID string, opts *AssociateOptions) (map[string]any, error) {
	relation := "related_to"
	args := map[string]any{"from_id": fromID, "to_id": toID}
	if opts != nil {
		if opts.Relation != "" {
			relation = opts.Relation
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	args["relation"] = relation
	return m.CallTool(ctx, "associate", args)
}

// Resolve follows a memory's supersession chain to its canonical head ("resolve").
func (m *McpClient) Resolve(ctx context.Context, memoryID, purpose string) (map[string]any, error) {
	args := map[string]any{"id": memoryID}
	if purpose != "" {
		args["purpose"] = purpose
	}
	return m.CallTool(ctx, "resolve", args)
}

// SummariseOptions configures Summarise.
type SummariseOptions struct {
	// IDs are MemoryItem UUIDs to summarise.
	IDs []string
	// SessionID summarises all MemoryItems in the session.
	SessionID string
	// Scope is a scope label override.
	Scope string
	// Contents are explicit source content strings to summarise.
	Contents []string
	// Purpose is the declared purpose token.
	Purpose string
}

// Summarise produces a governed, provenance-stamped summary ("summarise").
func (m *McpClient) Summarise(ctx context.Context, opts *SummariseOptions) (map[string]any, error) {
	args := map[string]any{}
	if opts != nil {
		if len(opts.IDs) > 0 {
			args["ids"] = opts.IDs
		}
		if opts.SessionID != "" {
			args["session_id"] = opts.SessionID
		}
		if opts.Scope != "" {
			args["scope"] = opts.Scope
		}
		if len(opts.Contents) > 0 {
			args["contents"] = opts.Contents
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	return m.CallTool(ctx, "summarise", args)
}

// NlQueryOptions configures NlQuery.
type NlQueryOptions struct {
	// Purpose is the declared purpose token for the audit trail.
	Purpose string
	// Interpret pipes the result through the LLM interpretation layer.
	Interpret bool
}

// NlQuery executes a natural-language query translated to SQL and executed ("nl_query").
func (m *McpClient) NlQuery(ctx context.Context, query string, opts *NlQueryOptions) (map[string]any, error) {
	args := map[string]any{"query": query, "interpret": false}
	if opts != nil {
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
		args["interpret"] = opts.Interpret
	}
	return m.CallTool(ctx, "nl_query", args)
}

// McpEraseSubjectOptions configures EraseSubject.
type McpEraseSubjectOptions struct {
	// Reason recorded on the erasure receipt. Defaults to "gdpr-art17-request".
	Reason string
}

// EraseSubject performs a GDPR Art. 17 crypto-shred erasure ("erase_subject").
func (m *McpClient) EraseSubject(ctx context.Context, subject string, opts *McpEraseSubjectOptions) (map[string]any, error) {
	reason := "gdpr-art17-request"
	if opts != nil && opts.Reason != "" {
		reason = opts.Reason
	}
	return m.CallTool(ctx, "erase_subject", map[string]any{"subject": subject, "reason": reason})
}

// IngestMediaOptions configures IngestMedia.
type IngestMediaOptions struct {
	// Modality is the media modality: image, audio, video, or text. Defaults to "image".
	Modality string
	// Codec is the MIME/codec of the payload.
	Codec string
	// BytesB64 is base64-encoded media bytes. Provide this OR Text.
	BytesB64 string
	// Text is a text payload to embed directly. Provide this OR BytesB64.
	Text string
	// TenantID is the owning agency/organization id.
	TenantID string
	// PartitionKey is an explicit partition key.
	PartitionKey string
}

// IngestMedia ingests an image/audio/video (base64) or text payload ("ingest_media").
func (m *McpClient) IngestMedia(ctx context.Context, objectType string, opts *IngestMediaOptions) (map[string]any, error) {
	modality := "image"
	args := map[string]any{"object_type": objectType}
	if opts != nil {
		if opts.Modality != "" {
			modality = opts.Modality
		}
		if opts.Codec != "" {
			args["codec"] = opts.Codec
		}
		if opts.BytesB64 != "" {
			args["bytes_b64"] = opts.BytesB64
		}
		if opts.Text != "" {
			args["text"] = opts.Text
		}
		if opts.TenantID != "" {
			args["tenant_id"] = opts.TenantID
		}
		if opts.PartitionKey != "" {
			args["partition_key"] = opts.PartitionKey
		}
	}
	args["modality"] = modality
	return m.CallTool(ctx, "ingest_media", args)
}

// SimilarMultimodalOptions configures SimilarMultimodal.
type SimilarMultimodalOptions struct {
	// TopK caps the response. Defaults to 10.
	TopK int
	// Modality is the embedding modality to rank by. Defaults to "text".
	Modality string
	// Purpose is the declared purpose token.
	Purpose string
}

// SimilarMultimodal runs a governed cross-modal similarity search (ADR-106, "similar_multimodal").
func (m *McpClient) SimilarMultimodal(ctx context.Context, entityType, entityID string, opts *SimilarMultimodalOptions) (map[string]any, error) {
	topK := 10
	modality := "text"
	args := map[string]any{"entity_type": entityType, "id": entityID}
	if opts != nil {
		if opts.TopK > 0 {
			topK = opts.TopK
		}
		if opts.Modality != "" {
			modality = opts.Modality
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	args["top_k"] = topK
	args["modality"] = modality
	return m.CallTool(ctx, "similar_multimodal", args)
}

// McpHybridSearchOptions configures HybridSearch.
type McpHybridSearchOptions struct {
	// TopK caps the response. Defaults to 10.
	TopK int
	// Purpose is the declared purpose token.
	Purpose string
}

// HybridSearch runs governed BM25 + vector retrieval fused via RRF ("hybrid_search").
func (m *McpClient) HybridSearch(ctx context.Context, entityType, query string, opts *McpHybridSearchOptions) (map[string]any, error) {
	topK := 10
	args := map[string]any{"entity_type": entityType, "query": query}
	if opts != nil {
		if opts.TopK > 0 {
			topK = opts.TopK
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	args["top_k"] = topK
	return m.CallTool(ctx, "hybrid_search", args)
}

// PathsBetweenOptions configures PathsBetween.
type PathsBetweenOptions struct {
	// MaxHops caps the path length in hops (1-16). Defaults to 4.
	MaxHops int
	// Purpose is the declared purpose token.
	Purpose string
}

// PathsBetween walks the governed relationship/identity graph ("paths_between").
func (m *McpClient) PathsBetween(ctx context.Context, fromID, toID string, opts *PathsBetweenOptions) (map[string]any, error) {
	maxHops := 4
	args := map[string]any{"from": fromID, "to": toID}
	if opts != nil {
		if opts.MaxHops > 0 {
			maxHops = opts.MaxHops
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	args["max_hops"] = maxHops
	return m.CallTool(ctx, "paths_between", args)
}

// ListLinkTypes lists every governed edge type defined in the ontology ("list_link_types").
func (m *McpClient) ListLinkTypes(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "list_link_types", map[string]any{})
}

// ServerHealth returns a readiness snapshot for an ops agent ("server_health").
func (m *McpClient) ServerHealth(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "server_health", map[string]any{})
}

// JobStatus lists continuous detection jobs with live status ("job_status").
func (m *McpClient) JobStatus(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "job_status", map[string]any{})
}

// Metrics returns operational counters for a monitoring agent ("metrics").
func (m *McpClient) Metrics(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "metrics", map[string]any{})
}

// ListRules lists detection rules (ADR-162, "list_rules").
func (m *McpClient) ListRules(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "list_rules", map[string]any{})
}

// McpCreateRuleOptions configures CreateRule.
type McpCreateRuleOptions struct {
	// Severity is the rule severity (e.g. low/medium/high/critical).
	Severity string
	// Description is an optional rule description.
	Description string
	// Purpose is the declared purpose token. Defaults to "security".
	Purpose string
}

// CreateRule creates a detection rule (ADR-162, "create_rule").
func (m *McpClient) CreateRule(ctx context.Context, name, conditionSQL string, opts *McpCreateRuleOptions) (map[string]any, error) {
	purpose := "security"
	args := map[string]any{"name": name, "condition_sql": conditionSQL}
	if opts != nil {
		if opts.Severity != "" {
			args["severity"] = opts.Severity
		}
		if opts.Description != "" {
			args["description"] = opts.Description
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	args["purpose"] = purpose
	return m.CallTool(ctx, "create_rule", args)
}

// ImportSigma imports a Sigma detection rule (YAML) ("import_sigma").
func (m *McpClient) ImportSigma(ctx context.Context, sigmaYAML, purpose string) (map[string]any, error) {
	if purpose == "" {
		purpose = "security"
	}
	return m.CallTool(ctx, "import_sigma", map[string]any{"yaml": sigmaYAML, "purpose": purpose})
}

// ListJobs lists all registered detection jobs with live status ("list_jobs").
func (m *McpClient) ListJobs(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "list_jobs", map[string]any{})
}

// ScheduleJob triggers an immediate run of a named detection job ("schedule_job").
func (m *McpClient) ScheduleJob(ctx context.Context, name string) (map[string]any, error) {
	return m.CallTool(ctx, "schedule_job", map[string]any{"name": name})
}

// ListWorkflows lists all registered workflow definitions ("list_workflows").
func (m *McpClient) ListWorkflows(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "list_workflows", map[string]any{})
}

// RunWorkflow starts a workflow execution by name ("run_workflow").
func (m *McpClient) RunWorkflow(ctx context.Context, name string) (map[string]any, error) {
	return m.CallTool(ctx, "run_workflow", map[string]any{"name": name})
}

// WorkflowStatus queries step-level status of a workflow run ("workflow_status").
func (m *McpClient) WorkflowStatus(ctx context.Context, runID string) (map[string]any, error) {
	return m.CallTool(ctx, "workflow_status", map[string]any{"run_id": runID})
}

// TraceCryptoOptions configures TraceCrypto.
type TraceCryptoOptions struct {
	// MaxHops is the maximum hop depth (1-10). Defaults to 5.
	MaxHops int
	// MinAmount filters edges below this transferred amount.
	MinAmount float64
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// TraceCrypto follows a cryptocurrency address hop-by-hop ("trace_crypto").
func (m *McpClient) TraceCrypto(ctx context.Context, address string, opts *TraceCryptoOptions) (map[string]any, error) {
	maxHops := 5
	purpose := "analytics"
	minAmount := 0.0
	if opts != nil {
		if opts.MaxHops > 0 {
			maxHops = opts.MaxHops
		}
		if opts.MinAmount != 0 {
			minAmount = opts.MinAmount
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "trace_crypto", map[string]any{
		"address": address, "max_hops": maxHops, "min_amount": minAmount, "purpose": purpose,
	})
}

// BeneficialOwnershipOptions configures BeneficialOwnership.
type BeneficialOwnershipOptions struct {
	// MaxDepth is the maximum depth to traverse (1-8). Defaults to 6.
	MaxDepth int
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// BeneficialOwnership traces the beneficial ownership chain for a party ("beneficial_ownership").
func (m *McpClient) BeneficialOwnership(ctx context.Context, party string, opts *BeneficialOwnershipOptions) (map[string]any, error) {
	maxDepth := 6
	purpose := "analytics"
	if opts != nil {
		if opts.MaxDepth > 0 {
			maxDepth = opts.MaxDepth
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "beneficial_ownership", map[string]any{
		"party": party, "max_depth": maxDepth, "purpose": purpose,
	})
}

// ReconstructWireOptions configures ReconstructWire.
type ReconstructWireOptions struct {
	// TolerancePct is the amount tolerance percentage for matching splits. Defaults to 5.0.
	TolerancePct float64
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// ReconstructWire reconstructs a wire-transfer chain for an account ("reconstruct_wire").
func (m *McpClient) ReconstructWire(ctx context.Context, account string, opts *ReconstructWireOptions) (map[string]any, error) {
	tolerancePct := 5.0
	purpose := "analytics"
	if opts != nil {
		if opts.TolerancePct != 0 {
			tolerancePct = opts.TolerancePct
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "reconstruct_wire", map[string]any{
		"account": account, "tolerance_pct": tolerancePct, "purpose": purpose,
	})
}

// TraceHawalaOptions configures TraceHawala.
type TraceHawalaOptions struct {
	// MaxHops is the maximum network hops (1-10). Defaults to 5.
	MaxHops int
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// TraceHawala traces informal hawala value-transfer networks ("trace_hawala").
func (m *McpClient) TraceHawala(ctx context.Context, seed string, opts *TraceHawalaOptions) (map[string]any, error) {
	maxHops := 5
	purpose := "analytics"
	if opts != nil {
		if opts.MaxHops > 0 {
			maxHops = opts.MaxHops
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "trace_hawala", map[string]any{
		"seed": seed, "max_hops": maxHops, "purpose": purpose,
	})
}

// GeofenceOptions configures Geofence.
type GeofenceOptions struct {
	// RadiusM is the radius in metres. Defaults to 1000.
	RadiusM float64
	// TargetType is the entity type to search. Defaults to "MovementEvent".
	TargetType string
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// Geofence runs a spatial fence query over a circular area ("geofence").
func (m *McpClient) Geofence(ctx context.Context, lat, lon float64, opts *GeofenceOptions) (map[string]any, error) {
	radiusM := 1000.0
	targetType := "MovementEvent"
	purpose := "analytics"
	if opts != nil {
		if opts.RadiusM != 0 {
			radiusM = opts.RadiusM
		}
		if opts.TargetType != "" {
			targetType = opts.TargetType
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "geofence", map[string]any{
		"lat": lat, "lon": lon, "radius_m": radiusM, "target_type": targetType, "purpose": purpose,
	})
}

// ResolveEntityIdentity resolves the canonical identity cluster ("resolve_entity_identity").
func (m *McpClient) ResolveEntityIdentity(ctx context.Context, identity, purpose string) (map[string]any, error) {
	if purpose == "" {
		purpose = "analytics"
	}
	return m.CallTool(ctx, "resolve_entity_identity", map[string]any{"identity": identity, "purpose": purpose})
}

// DetectCommunitiesOptions configures DetectCommunities.
type DetectCommunitiesOptions struct {
	// Algo is the community algorithm: louvain (default) or leiden.
	Algo string
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// DetectCommunities runs Louvain/Leiden community detection ("detect_communities").
func (m *McpClient) DetectCommunities(ctx context.Context, entityType string, opts *DetectCommunitiesOptions) (map[string]any, error) {
	algo := "louvain"
	purpose := "analytics"
	if opts != nil {
		if opts.Algo != "" {
			algo = opts.Algo
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "detect_communities", map[string]any{
		"entity_type": entityType, "algo": algo, "purpose": purpose,
	})
}

// RankKeyNodesOptions configures RankKeyNodes.
type RankKeyNodesOptions struct {
	// Metric is the ranking metric: pagerank (default), betweenness, closeness, eigenvector.
	Metric string
	// Damping is the PageRank damping factor. Defaults to 0.85.
	Damping float64
	// MaxIter is the max iterations for PageRank. Defaults to 20.
	MaxIter int
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// RankKeyNodes ranks entities by PageRank or centrality metric ("rank_key_nodes").
func (m *McpClient) RankKeyNodes(ctx context.Context, entityType string, opts *RankKeyNodesOptions) (map[string]any, error) {
	metric := "pagerank"
	damping := 0.85
	maxIter := 20
	purpose := "analytics"
	if opts != nil {
		if opts.Metric != "" {
			metric = opts.Metric
		}
		if opts.Damping != 0 {
			damping = opts.Damping
		}
		if opts.MaxIter > 0 {
			maxIter = opts.MaxIter
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "rank_key_nodes", map[string]any{
		"entity_type": entityType, "metric": metric, "damping": damping, "max_iter": maxIter, "purpose": purpose,
	})
}

// HubAuthorityOptions configures HubAuthority.
type HubAuthorityOptions struct {
	// MaxIter is the max HITS iterations. Defaults to 20.
	MaxIter int
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// HubAuthority computes HITS hub/authority scores for an entity type ("hub_authority").
func (m *McpClient) HubAuthority(ctx context.Context, entityType string, opts *HubAuthorityOptions) (map[string]any, error) {
	maxIter := 20
	purpose := "analytics"
	if opts != nil {
		if opts.MaxIter > 0 {
			maxIter = opts.MaxIter
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "hub_authority", map[string]any{
		"entity_type": entityType, "max_iter": maxIter, "purpose": purpose,
	})
}

// PredictLinksOptions configures PredictLinks.
type PredictLinksOptions struct {
	// FromID is the source entity id (optional).
	FromID string
	// ToID is the target entity id (optional).
	ToID string
	// Method is the link-prediction method: common_neighbors (default), jaccard, adamic_adar.
	Method string
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// PredictLinks scores candidate edges between entities ("predict_links").
func (m *McpClient) PredictLinks(ctx context.Context, entityType string, opts *PredictLinksOptions) (map[string]any, error) {
	method := "common_neighbors"
	purpose := "analytics"
	args := map[string]any{"entity_type": entityType}
	if opts != nil {
		if opts.FromID != "" {
			args["from_id"] = opts.FromID
		}
		if opts.ToID != "" {
			args["to_id"] = opts.ToID
		}
		if opts.Method != "" {
			method = opts.Method
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	args["method"] = method
	args["purpose"] = purpose
	return m.CallTool(ctx, "predict_links", args)
}

// FindScc finds strongly connected components in an entity type's graph ("find_scc").
func (m *McpClient) FindScc(ctx context.Context, entityType, purpose string) (map[string]any, error) {
	if purpose == "" {
		purpose = "analytics"
	}
	return m.CallTool(ctx, "find_scc", map[string]any{"entity_type": entityType, "purpose": purpose})
}

// ScreenSanctionsOptions configures ScreenSanctions.
type ScreenSanctionsOptions struct {
	// Threshold is the minimum match confidence (0.0-1.0).
	Threshold float64
	// HasThreshold reports whether Threshold should be sent (0.0 is a valid threshold).
	HasThreshold bool
	// Purpose is the declared purpose token. Defaults to "compliance_review".
	Purpose string
}

// ScreenSanctions screens a name/entity against sanctions lists ("screen_sanctions").
func (m *McpClient) ScreenSanctions(ctx context.Context, name string, opts *ScreenSanctionsOptions) (map[string]any, error) {
	purpose := "compliance_review"
	args := map[string]any{"name": name}
	if opts != nil {
		if opts.HasThreshold {
			args["threshold"] = opts.Threshold
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	args["purpose"] = purpose
	return m.CallTool(ctx, "screen_sanctions", args)
}

// AggregateStatsOptions configures AggregateStats.
type AggregateStatsOptions struct {
	// Agg is the aggregate function: COUNT (default), SUM, AVG.
	Agg string
	// Column to aggregate. Defaults to "*".
	Column string
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// AggregateStats runs a governed aggregate query over an entity type ("aggregate_stats").
func (m *McpClient) AggregateStats(ctx context.Context, entityType string, opts *AggregateStatsOptions) (map[string]any, error) {
	agg := "COUNT"
	column := "*"
	purpose := "analytics"
	if opts != nil {
		if opts.Agg != "" {
			agg = opts.Agg
		}
		if opts.Column != "" {
			column = opts.Column
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "aggregate_stats", map[string]any{
		"entity_type": entityType, "agg": agg, "column": column, "purpose": purpose,
	})
}

// InvestigateEntity runs a composite profile + timeline + connections + risk investigation
// ("investigate_entity").
func (m *McpClient) InvestigateEntity(ctx context.Context, entityType, entityID, purpose string) (map[string]any, error) {
	if purpose == "" {
		purpose = "security_incident"
	}
	return m.CallTool(ctx, "investigate_entity", map[string]any{
		"entity_type": entityType, "id": entityID, "purpose": purpose,
	})
}

// FindThreats runs a composite threat hunt over an entity type ("find_threats").
func (m *McpClient) FindThreats(ctx context.Context, entityType, purpose string) (map[string]any, error) {
	if purpose == "" {
		purpose = "security_incident"
	}
	return m.CallTool(ctx, "find_threats", map[string]any{"entity_type": entityType, "purpose": purpose})
}

// SearchVideoFramesOptions configures SearchVideoFrames.
type SearchVideoFramesOptions struct {
	// MediaType is the entity type to search. Defaults to "VideoFrame".
	MediaType string
	// TopK caps the response. Defaults to 20.
	TopK int
	// Purpose is the declared purpose token. Defaults to "security_incident".
	Purpose string
}

// SearchVideoFrames returns video frames/segments similar to a seed frame ("search_video_frames").
func (m *McpClient) SearchVideoFrames(ctx context.Context, queryID string, opts *SearchVideoFramesOptions) (map[string]any, error) {
	mediaType := "VideoFrame"
	topK := 20
	purpose := "security_incident"
	if opts != nil {
		if opts.MediaType != "" {
			mediaType = opts.MediaType
		}
		if opts.TopK > 0 {
			topK = opts.TopK
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "search_video_frames", map[string]any{
		"query_id": queryID, "media_type": mediaType, "top_k": topK, "purpose": purpose,
	})
}

// FaceMatchOptions configures FaceMatch.
type FaceMatchOptions struct {
	// Threshold is the minimum match confidence (0.0-1.0). Defaults to 0.8.
	Threshold float64
	// TopK caps the response. Defaults to 10.
	TopK int
	// Purpose is the declared purpose token. Defaults to "security_incident".
	Purpose string
}

// FaceMatch matches a probe face against indexed media ("face_match"; GATED until ADR-155 ships).
func (m *McpClient) FaceMatch(ctx context.Context, probeID string, opts *FaceMatchOptions) (map[string]any, error) {
	threshold := 0.8
	topK := 10
	purpose := "security_incident"
	if opts != nil {
		if opts.Threshold != 0 {
			threshold = opts.Threshold
		}
		if opts.TopK > 0 {
			topK = opts.TopK
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	return m.CallTool(ctx, "face_match", map[string]any{
		"probe_id": probeID, "threshold": threshold, "top_k": topK, "purpose": purpose,
	})
}
