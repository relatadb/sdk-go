package relata

import (
	"context"
)

// McpClient is the synchronous Model Context Protocol client. It wraps
// /v1/mcp/initialize, /v1/mcp/tools, /v1/mcp/tools/call, and provides typed wrappers for retained MCP tools. Responses are returned unwrapped
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
	// Filters is an optional set of field filters, sent as a JSON object. The
	// server (mcp_tool_get_entities, crates/relata-cli/src/serve/mcp.rs) pushes
	// them into the SQL WHERE clause before LIMIT (#5533): string values match
	// exactly but case-insensitively, numbers/booleans exactly. "filters" must
	// be a JSON object; a bare JSON string is silently ignored (#4665).
	Filters map[string]any
	// Limit caps the response. Defaults to 50.
	Limit int
}

// SearchEntitiesOptions configures SearchEntities.
type SearchEntitiesOptions struct {
	// ObjectType restricts the search to one type.
	ObjectType string
}

// FindInSocialCorpusOptions configures FindInSocialCorpus.
type FindInSocialCorpusOptions struct {
	// User restricts the search to one user (matched against UserField).
	User string
	// UserField is the row field User is matched against. Defaults to "user".
	UserField string
	// BlobField, if set, restricts the search to hits carrying this media blob field.
	BlobField string
	// TopK caps the response. Defaults to 20.
	TopK int
}

// LookupIdentityOptions configures LookupIdentity.
type LookupIdentityOptions struct {
	// Purpose is the declared purpose token. Defaults to "analytics".
	Purpose string
}

// GetTimelineOptions configures GetTimeline.
type GetTimelineOptions struct {
	// Limit caps the response (server default 100, capped at 1000).
	Limit int
}

// FindConnectionsOptions configures FindConnections.
type FindConnectionsOptions struct {
	// Limit caps the response (default 50, max 200).
	Limit int
}

// GetRelationshipsOptions configures GetRelationships.
type GetRelationshipsOptions struct {
	// Predicate filters KnowledgeTriple rows by predicate (lowercase-matched).
	Predicate string
	// Object filters KnowledgeTriple rows by object (lowercase-matched).
	Object string
	// Source filters KnowledgeTriple rows by source/source_filename (lowercase-matched).
	Source string
	// Limit caps the response. Defaults to 50.
	Limit int
}

// AddCaseNoteOptions configures AddCaseNote.
type AddCaseNoteOptions struct {
	// Author is accepted for API stability but currently has no server-side
	// effect: mcp_tool_add_case_note_with_gate (crates/relata-cli/src/serve/mcp/doc_writes.rs)
	// never reads an "author" field (#4667, same class as the already-fixed TS #4657).
	Author string
	// Category labels the note (e.g. "finding"). Defaults to "finding" server-side.
	Category string
	// EntitiesMentioned attaches a list of entity ids the note references.
	EntitiesMentioned []string
}

// GetAuditTrailOptions configures GetAuditTrail.
type GetAuditTrailOptions struct {
	// PrincipalFilter filters entries to one principal (substring-matched).
	PrincipalFilter string
	// Limit caps the response (server default 100, capped at 1000).
	Limit int
	// Purpose is the declared purpose token, or "all" to bypass the purpose
	// allowlist. Defaults to "analytics".
	Purpose string
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
	// SessionID links the memory to an agent session.
	SessionID string
	// Fields contains extra metadata fields accepted by the target memory type.
	Fields map[string]any
}

// RecallOptions configures the MCP Recall tool.
//
// The five pointer fields are the ADR-145 retrieval-quality operators
// (MinConfidence = CONFIDENCE, RecencyHalfLifeSecs = RECENCY, BudgetTokens =
// BUDGET, StabilityDays = FORGETTING_CURVE, CancelThreshold = CANCEL_WHEN).
// Pointers so "unset" (let the server default) is distinguishable from a
// meaningful zero value; nil is omitted from the tool call.
type RecallOptions struct {
	// TopK caps the response. Defaults to 5.
	TopK int
	// MinConfidence filters results to confidence >= *MinConfidence.
	MinConfidence *float64
	// RecencyHalfLifeSecs applies exponential score decay with this half-life.
	RecencyHalfLifeSecs *float64
	// BudgetTokens stops result emission once cumulative token cost exceeds it.
	BudgetTokens *uint64
	// StabilityDays is the Ebbinghaus forgetting-curve stability parameter.
	StabilityDays *float64
	// CancelThreshold short-circuits the scan once a hit exceeds it.
	CancelThreshold *float64
	// SessionID limits recall to one agent session.
	SessionID string
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
	if err := m.c.postJSON(ctx, "/v1/mcp/initialize", map[string]any{"client_id": clientID, "version": version}, &resp); err != nil {
		return nil, err
	}
	return unwrapMCP(resp), nil
}

// ListTools lists every MCP tool the server exposes (30+).
func (m *McpClient) ListTools(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := m.c.get(ctx, "/v1/mcp/tools", &resp); err != nil {
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
	if err := m.c.postJSON(ctx, "/v1/mcp/tools/call", payload, &resp); err != nil {
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
		if len(opts.Filters) > 0 {
			args["filters"] = opts.Filters
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

// FindInSocialCorpus searches the ingested social-media corpus ("find_in_social_corpus").
//
// The server's mcp_tool_find_in_social_corpus (crates/relata-cli/src/serve/mcp.rs)
// requires objectType to be a registered ontology type name (e.g. "SocialPost")
// and reads the free-text query under a separate text_query key. This wrapper
// previously mapped the caller's search text onto object_type and dropped the
// real query entirely (#4666, same treatment as the already-fixed TS #4644).
func (m *McpClient) FindInSocialCorpus(ctx context.Context, objectType, textQuery string, opts *FindInSocialCorpusOptions) (map[string]any, error) {
	topK := 20
	args := map[string]any{"object_type": objectType}
	if textQuery != "" {
		args["text_query"] = textQuery
	}
	if opts != nil {
		if opts.User != "" {
			args["user"] = opts.User
		}
		if opts.UserField != "" {
			args["user_field"] = opts.UserField
		}
		if opts.BlobField != "" {
			args["blob_field"] = opts.BlobField
		}
		if opts.TopK > 0 {
			topK = opts.TopK
		}
	}
	args["top_k"] = topK
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
//
// mcp_tool_get_timeline (crates/relata-cli/src/serve/mcp.rs) has no time-range
// concept — SinceNS/UntilNS were previously accepted but structurally
// unreachable (opts was discarded outright); the real limit knob is now wired
// through instead (#4667, same class as the already-fixed TS #4649).
func (m *McpClient) GetTimeline(ctx context.Context, entityID, purpose string, opts *GetTimelineOptions) (map[string]any, error) {
	args := map[string]any{"entity": entityID, "purpose": purpose}
	if opts != nil && opts.Limit > 0 {
		args["limit"] = opts.Limit
	}
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

// GetRelationships returns KnowledgeTriple rows matching entityID as the subject
// (plus any additional filters in opts).
//
// mcp_tool_get_relationships (crates/relata-cli/src/serve/mcp.rs) never reads
// entity_id/depth (there is no hop-depth graph walk here, only a flat triple
// filter) — it reads purpose/limit and lowercase subject/predicate/object/source
// filters; Depth was previously accepted but structurally unreachable (opts was
// discarded outright). The real predicate/object/source/limit filters are now
// wired through (#4667, same class as the already-fixed TS #4646).
func (m *McpClient) GetRelationships(ctx context.Context, entityID, purpose string, opts *GetRelationshipsOptions) (map[string]any, error) {
	args := map[string]any{"subject": entityID, "purpose": purpose}
	if opts != nil {
		if opts.Predicate != "" {
			args["predicate"] = opts.Predicate
		}
		if opts.Object != "" {
			args["object"] = opts.Object
		}
		if opts.Source != "" {
			args["source"] = opts.Source
		}
		if opts.Limit > 0 {
			args["limit"] = opts.Limit
		}
	}
	return m.CallTool(ctx, "get_relationships", args)
}

// AddCaseNote appends an investigative note to a case.
func (m *McpClient) AddCaseNote(ctx context.Context, caseID, note string, opts *AddCaseNoteOptions) (map[string]any, error) {
	args := map[string]any{"case_id": caseID, "note": note}
	if opts != nil {
		if opts.Author != "" {
			args["author"] = opts.Author
		}
		if opts.Category != "" {
			args["category"] = opts.Category
		}
		if len(opts.EntitiesMentioned) > 0 {
			args["entities_mentioned"] = opts.EntitiesMentioned
		}
	}
	return m.CallTool(ctx, "add_case_note", args)
}

// GetAuditTrail returns the query audit log, optionally filtered by principal.
//
// mcp_tool_get_audit_trail (crates/relata-cli/src/serve/mcp.rs) never reads
// case_id/entity_id — it filters by principal_filter/limit/purpose, and treats
// purpose: "all" as a sentinel that bypasses the usual purpose allowlist check.
// CaseID/EntityID were previously accepted but structurally unreachable (opts
// was discarded outright); the real filters are now wired through (#4667, same
// class as the already-fixed TS #4650).
func (m *McpClient) GetAuditTrail(ctx context.Context, opts *GetAuditTrailOptions) (map[string]any, error) {
	args := map[string]any{}
	if opts != nil {
		if opts.PrincipalFilter != "" {
			args["principal_filter"] = opts.PrincipalFilter
		}
		if opts.Limit > 0 {
			args["limit"] = opts.Limit
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
	}
	return m.CallTool(ctx, "get_audit_trail", args)
}

// GetCaseSummaryOptions configures GetCaseSummary.
type GetCaseSummaryOptions struct {
	// IncludeGraph includes knowledge-graph stats in the summary. Defaults to true.
	IncludeGraph *bool
	// IncludeNotes includes analyst CaseAnnotation notes. Defaults to true.
	IncludeNotes *bool
	// IncludeAnswers includes stored RAG answers. Defaults to true.
	IncludeAnswers *bool
}

// GetCaseSummary returns an LLM-generated narrative summary of the tenant's data
// inventory + knowledge-graph stats + analyst notes ("get_case_summary").
//
// NOT case-scoped: the server's mcp_tool_get_case_summary
// (crates/relata-cli/src/serve/mcp.rs) never reads a case_id argument anywhere —
// there is no case-scoping capability in this handler at all, so a caller-supplied
// case id had no effect (#4658, same treatment as the already-fixed TS #4651). This
// wrapper drops that misleading parameter and exposes the real
// include_graph/include_notes/include_answers toggles the handler actually reads.
func (m *McpClient) GetCaseSummary(ctx context.Context, purpose string, opts *GetCaseSummaryOptions) (map[string]any, error) {
	args := map[string]any{"purpose": purpose}
	if opts != nil {
		if opts.IncludeGraph != nil {
			args["include_graph"] = *opts.IncludeGraph
		}
		if opts.IncludeNotes != nil {
			args["include_notes"] = *opts.IncludeNotes
		}
		if opts.IncludeAnswers != nil {
			args["include_answers"] = *opts.IncludeAnswers
		}
	}
	return m.CallTool(ctx, "get_case_summary", args)
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

// IngestDocumentOptions configures IngestDocument.
type IngestDocumentOptions struct {
	// Label overrides the derived document label. Defaults to Source.
	Label string
	// Confidence in the extracted content (0-1). Defaults to 0.9.
	Confidence float64
	// HasConfidence reports whether Confidence should be sent (0.0 is a valid confidence).
	HasConfidence bool
	// Entities are pre-extracted entity objects to attach to the ingest.
	Entities []map[string]any
	// Relations are pre-extracted relation objects to attach to the ingest.
	Relations []map[string]any
}

// IngestDocument ingests a document via MCP ("ingest_document").
//
// The server's mcp_tool_ingest_document_with_gate
// (crates/relata-cli/src/serve/mcp/doc_writes.rs) reads a flat
// source/text/label/confidence/entities/relations shape: source is the short
// document-source identifier/filename, text is the document body. This wrapper
// previously sent chunksJSONL/manifestJSON under those two keys inverted
// (manifestJSON as source, chunksJSONL as text), so every call persisted
// semantically scrambled data (#4662, same treatment as the already-fixed TS
// #4655). Callers now pass source/text directly.
func (m *McpClient) IngestDocument(ctx context.Context, source, text, purpose string, opts *IngestDocumentOptions) (map[string]any, error) {
	args := map[string]any{"source": source, "text": text, "purpose": purpose}
	if opts != nil {
		if opts.Label != "" {
			args["label"] = opts.Label
		}
		if opts.HasConfidence {
			args["confidence"] = opts.Confidence
		}
		if len(opts.Entities) > 0 {
			args["entities"] = opts.Entities
		}
		if len(opts.Relations) > 0 {
			args["relations"] = opts.Relations
		}
	}
	return m.CallTool(ctx, "ingest_document", args)
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
	args := map[string]any{
		"content":      content,
		"purpose":      purpose,
		"confidence":   confidence,
		"memory_class": memoryClass,
	}
	if opts != nil {
		if opts.SessionID != "" {
			args["session_id"] = opts.SessionID
		}
		if len(opts.Fields) > 0 {
			args["fields"] = opts.Fields
		}
	}
	return m.CallTool(ctx, "remember", args)
}

// Recall is the "recall" MCP tool. See RecallOptions for the ADR-145
// retrieval-quality operators (#2674). The returned envelope carries the
// read-only recall_cost_tokens/cancelled fields once unwrapped (unwrapMCP).
func (m *McpClient) Recall(ctx context.Context, query, purpose string, opts *RecallOptions) (map[string]any, error) {
	topK := 5
	args := map[string]any{"query": query, "purpose": purpose}
	if opts != nil {
		if opts.TopK > 0 {
			topK = opts.TopK
		}
		if opts.MinConfidence != nil {
			args["min_confidence"] = *opts.MinConfidence
		}
		if opts.RecencyHalfLifeSecs != nil {
			args["recency_half_life_secs"] = *opts.RecencyHalfLifeSecs
		}
		if opts.BudgetTokens != nil {
			args["budget_tokens"] = *opts.BudgetTokens
		}
		if opts.StabilityDays != nil {
			args["stability_days"] = *opts.StabilityDays
		}
		if opts.CancelThreshold != nil {
			args["cancel_threshold"] = *opts.CancelThreshold
		}
		if opts.SessionID != "" {
			args["session_id"] = opts.SessionID
		}
	}
	args["top_k"] = topK
	return m.CallTool(ctx, "recall", args)
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

// Summarise produces a deterministic extractive, provenance-stamped summary.
// For model-composed summaries use SummariseMemory with an application callback.
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

// McpEraseSubjectOptions configures EraseSubject.
type McpEraseSubjectOptions struct {
	// Reason recorded on the erasure receipt. Defaults to "gdpr-art17-request".
	Reason string
}

// EraseSubject performs a governed erasure ("erase_subject"); local receipts do not certify provider key destruction.
func (m *McpClient) EraseSubject(ctx context.Context, subject string, opts *McpEraseSubjectOptions) (map[string]any, error) {
	reason := "gdpr-art17-request"
	if opts != nil && opts.Reason != "" {
		reason = opts.Reason
	}
	return m.CallTool(ctx, "erase_subject", map[string]any{"subject": subject, "reason": reason})
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
	// Purpose is the declared purpose token. Defaults to "security".
	Purpose string
}

// CreateRule creates a detection rule (ADR-162, "create_rule").
//
// targetType is required: the underlying POST /v1/rules handler
// (crates/relata-cli/src/serve/rules.rs) 400s with "missing required field
// 'target_type'" when it is absent, and this wrapper never sent it (#4663, same
// treatment as the already-fixed TS #4656). Description is intentionally not
// exposed — rules.rs never reads a "description" field anywhere.
func (m *McpClient) CreateRule(ctx context.Context, name, conditionSQL, targetType string, opts *McpCreateRuleOptions) (map[string]any, error) {
	purpose := "security"
	args := map[string]any{"name": name, "condition_sql": conditionSQL, "target_type": targetType}
	if opts != nil {
		if opts.Severity != "" {
			args["severity"] = opts.Severity
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	args["purpose"] = purpose
	return m.CallTool(ctx, "create_rule", args)
}

// ListJobs lists detection jobs registered by this server with live status ("list_jobs").
func (m *McpClient) ListJobs(ctx context.Context) (map[string]any, error) {
	return m.CallTool(ctx, "list_jobs", map[string]any{})
}

// ScheduleJob requests an immediate run of a detection job known to this server ("schedule_job").
func (m *McpClient) ScheduleJob(ctx context.Context, name string) (map[string]any, error) {
	return m.CallTool(ctx, "schedule_job", map[string]any{"name": name})
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
