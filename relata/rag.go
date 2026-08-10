package relata

import "context"

// RagClient is a thin typed pass-through for `POST /rag/query` (#4523,
// #4514/ADR-0299). Per ADR-0298, sdks/python carries the canonical
// agentic-RAG implementation (the SDK-side iterative retrieve->evaluate->
// refine loop); this client stays a thin typed wrapper over the frozen
// /rag/query contract — no client-side agentic loop lives here, not full
// parity with sdks/python's relata.rag.
type RagClient struct {
	c *Client
}

// NewRagClient constructs a RagClient that backs onto the parent client's
// auth/tenant context. Mirrors the Python reference's RagClient.from_client.
func NewRagClient(c *Client) *RagClient {
	return &RagClient{c: c}
}

// RagDefaultTopK matches RAG_RETRIEVE_DEFAULT_TOP_K
// (crates/relata-query/src/parser.rs:2854).
const RagDefaultTopK = 8

// RagFilter is a single `[{field, op, value}]` filter clause — the standard
// WHERE-equivalent filter list #4514's contract accepts.
type RagFilter struct {
	Field string `json:"field"`
	Op    string `json:"op,omitempty"`
	Value any    `json:"value"`
}

// RagQueryRequest is the typed POST /rag/query body (#4514's exact
// parameter table — do not deviate without re-opening ADR-0299).
type RagQueryRequest struct {
	Query string `json:"query"`
	// Type is the object type to search (e.g. "DocumentChunk").
	Type string `json:"type"`
	// TopK caps hit count; 0 uses the server default (RagDefaultTopK).
	TopK int `json:"top_k,omitempty"`
	// Rerank dispatches to the sidecar cross-encoder.
	Rerank bool `json:"rerank,omitempty"`
	// SearchMode is "lexical" | "dense" | "hybrid" | "structural" (default
	// "hybrid"). "structural" (#4542) retrieves via the persisted
	// DocumentStructureNode table-of-contents index; the SDK-side multi-hop
	// agentic descent loop over that tree is Python-only (ADR-0298) — this
	// field is a thin pass-through string here, same as every other mode.
	SearchMode string `json:"search_mode,omitempty"`
	// EmbeddingSlot is "text" | "summary" | "keyword" | "question"
	// (default "text").
	EmbeddingSlot string `json:"embedding_slot,omitempty"`
	// Filters is the standard WHERE-equivalent filter list.
	Filters []RagFilter `json:"filters,omitempty"`
	// AsOf is an optional bi-temporal point-in-time query timestamp.
	AsOf string `json:"as_of,omitempty"`
	// Purpose is the declared purpose token. Falls back to the parent
	// client's DefaultPurpose when empty.
	Purpose string `json:"purpose"`
	// ExpandWindow requests sentence-window expansion via adjacency links.
	ExpandWindow bool `json:"expand_window,omitempty"`
	// GraphHops sets the graph-augmented expansion depth.
	GraphHops int `json:"graph_hops,omitempty"`
}

// RagHit is a single hit from POST /rag/query (#4514's per-hit contract
// table). BM25Score/VectorScore are always present and never fused into one
// number — SDK-side fusion/diversity selection depends on both being real,
// independent values.
type RagHit struct {
	BM25Score   float64  `json:"bm25_score"`
	VectorScore float64  `json:"vector_score"`
	RerankScore *float64 `json:"rerank_score"`
	ChunkID     string   `json:"chunk_id"`
	// ReportID is the source document id (expected rename to
	// document_source_id once #4495's DocumentSource naming lands on the
	// wire; carries the same value under either name).
	ReportID    string   `json:"report_id"`
	Text        string   `json:"text"`
	SectionPath []string `json:"section_path"`
	PageStart   int      `json:"page_start"`
	PageEnd     int      `json:"page_end"`
	PrevChunkID *string  `json:"prev_chunk_id"`
	NextChunkID *string  `json:"next_chunk_id"`
	EntityIDs   []string `json:"entity_ids"`
}

// RagQueryResponse is the response from POST /rag/query.
type RagQueryResponse struct {
	Hits []RagHit `json:"hits"`
}

// Query runs a governed retrieval via POST /rag/query (#4514's exact
// parameter table). If req.Purpose is empty it falls back to the parent
// client's DefaultPurpose; ErrPurposeRequired is returned if neither is set.
//
//	rag := relata.NewRagClient(client)
//	resp, err := rag.Query(ctx, relata.RagQueryRequest{
//	    Query: "graph databases", Type: "DocumentChunk", Purpose: "research",
//	})
func (r *RagClient) Query(ctx context.Context, req RagQueryRequest) (*RagQueryResponse, error) {
	purpose := req.Purpose
	if purpose == "" {
		purpose = r.c.defaultPurpose
	}
	if purpose == "" {
		return nil, ErrPurposeRequired
	}
	if req.TopK == 0 {
		req.TopK = RagDefaultTopK
	}
	if req.SearchMode == "" {
		req.SearchMode = "hybrid"
	}
	if req.EmbeddingSlot == "" {
		req.EmbeddingSlot = "text"
	}
	body := req
	body.Purpose = purpose
	if body.Filters == nil {
		body.Filters = []RagFilter{}
	}
	var out RagQueryResponse
	if err := r.c.postJSON(ctx, "/rag/query", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
