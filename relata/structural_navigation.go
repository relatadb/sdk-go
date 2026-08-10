package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Structural table-of-contents navigation — RAG epic SDK-side agentic
// retrieval strategy (#4542), Go parity port of
// sdks/python/relata/structural_navigation.py (#4581, epic #4576).
//
// The substrate half of #4542 (crates/relata-storage/src/document_ingest.rs
// document_structure_batch) derives a DocumentStructureNode tree from each
// document's section_path/page_start/page_end — one node per distinct
// heading-breadcrumb prefix, plus a synthetic root, each carrying a cheap
// LLM-free one-line Summary and its direct LeafChunkIDs.
// POST /rag/query's search_mode: structural (#4514/#4542,
// crates/relata-cli/src/serve/query/rag_query.rs) is a single-shot, one-hop
// approximation of tree navigation: it lexically matches node
// title/summary, then ranks within the matched node(s)' own leaf chunks.
//
// StructuralNavigator is the true multi-hop agent the ticket's design
// section describes: starting at the document root,
// NavigateStructuralTree repeatedly fetches the current node's children
// (FetchChildNodes), asks a ChildSelector strategy which subtree to
// descend into, and stops either when a level has no children (a leaf) or
// the strategy declines to descend further — then ranks that leaf's own
// LeafChunkIDs via a governed RAG_RETRIEVE call, exactly like every other
// /rag/query mode (same ACL/cell-masking enforcement — no bypass, since
// this still goes through Client.Query, the same governed /query surface
// /rag/query itself delegates to server-side).
//
// Per ADR-013, RelataDB has no server-side agent loop — the reasoning that
// picks which child to descend into is a caller-supplied ChildSelector,
// never called by RelataDB itself. LexicalChildSelector is a
// zero-dependency, LLM-free default (word-overlap between the question and
// each candidate's title/summary) so this module is usable standalone; an
// application wanting real judgement supplies its own LLM-backed selector
// with the same signature.

// DefaultMaxDepth is the hard ceiling on tree descent depth — a runaway
// ChildSelector (e.g. one that always returns a child even on a
// cyclic/malformed tree) cannot loop forever; #4542's node ids are acyclic
// by construction (a strict prefix-of-section_path tree), so this should
// never actually bind.
const DefaultMaxDepth = 12

// StructureNode is one DocumentStructureNode row (#4542), as returned by a
// plain `SELECT * FROM DocumentStructureNode` via Client.Query.
type StructureNode struct {
	NodeID       string
	ReportID     string
	ParentID     *string
	Title        string
	Depth        int
	PageStart    *int
	PageEnd      *int
	Summary      *string
	ChildCount   int
	LeafChunkIDs []string
}

// IsLeaf reports whether n has no children (ChildCount == 0).
func (n StructureNode) IsLeaf() bool {
	return n.ChildCount == 0
}

// structuralRowString coerces a JSON-decoded row value into a string,
// returning "" for nil.
func structuralRowString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// structuralRowStringPtr is structuralRowString, but returns nil for a nil
// value instead of "" — used for genuinely optional string fields
// (ParentID/Summary/PrevChunkID/NextChunkID) where nil and "" are distinct.
func structuralRowStringPtr(v any) *string {
	if v == nil {
		return nil
	}
	s := structuralRowString(v)
	return &s
}

// structuralRowIntPtr is intFromAny, but returns nil for a nil value
// instead of 0 — used for genuinely optional int fields
// (PageStart/PageEnd) where nil and 0 are distinct.
func structuralRowIntPtr(v any) *int {
	if v == nil {
		return nil
	}
	n := intFromAny(v)
	return &n
}

// structuralFloatFromAny coerces a JSON-decoded number (float64) into a
// float64, returning 0 for nil or non-numeric values.
func structuralFloatFromAny(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// StructureNodeFromRow builds a StructureNode from one /query result row
// (Client.Query's QueryResult.Rows element).
//
// LeafChunkIDs is stored server-side as a JSON array string
// (crates/relata-storage/src/document_ingest.rs — no native array
// CanonicalValue variant); this parses it back into a real slice,
// defaulting to nil on anything malformed rather than erroring, so a node
// with no directly-attached chunks (an internal/structural-only node)
// never breaks tree descent.
func StructureNodeFromRow(row map[string]any) StructureNode {
	var leafChunkIDs []string
	switch raw := row["leaf_chunk_ids"].(type) {
	case []any:
		leafChunkIDs = make([]string, 0, len(raw))
		for _, v := range raw {
			leafChunkIDs = append(leafChunkIDs, fmt.Sprint(v))
		}
	case string:
		if raw != "" {
			var parsed []any
			if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
				leafChunkIDs = make([]string, 0, len(parsed))
				for _, v := range parsed {
					leafChunkIDs = append(leafChunkIDs, fmt.Sprint(v))
				}
			}
		}
	}
	return StructureNode{
		NodeID:       structuralRowString(row["node_id"]),
		ReportID:     structuralRowString(row["report_id"]),
		ParentID:     structuralRowStringPtr(row["parent_id"]),
		Title:        structuralRowString(row["title"]),
		Depth:        intFromAny(row["depth"]),
		PageStart:    structuralRowIntPtr(row["page_start"]),
		PageEnd:      structuralRowIntPtr(row["page_end"]),
		Summary:      structuralRowStringPtr(row["summary"]),
		ChildCount:   intFromAny(row["child_count"]),
		LeafChunkIDs: leafChunkIDs,
	}
}

// ChildSelector is a caller-supplied reasoning strategy: given the question
// and the current level's candidate children, return the child to descend
// into, or nil to stop descending (the current node is judged good enough
// already). RelataDB never calls an LLM itself (ADR-013) — a real agentic
// strategy is always supplied by the application; see LexicalChildSelector
// for the zero-dependency default.
type ChildSelector func(question string, children []StructureNode) *StructureNode

var structuralWordRE = regexp.MustCompile(`[a-z0-9]+`)

func structuralTokenize(text string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, tok := range structuralWordRE.FindAllString(strings.ToLower(text), -1) {
		out[tok] = struct{}{}
	}
	return out
}

// LexicalChildSelector is the zero-dependency, LLM-free default
// ChildSelector: picks the child whose Title/Summary shares the most
// question words (simple token-overlap count), or nil when no child shares
// even one word with the question — in which case NavigateStructuralTree
// stops descending at the current node rather than picking an unrelated
// child.
//
// Not a substitute for real reasoning over the child summaries (an
// LLM-backed selector will pick better subtrees on paraphrased questions);
// this exists so the module is usable standalone and as the documented
// reference implementation of the ChildSelector contract.
func LexicalChildSelector(question string, children []StructureNode) *StructureNode {
	qTokens := structuralTokenize(question)
	if len(qTokens) == 0 {
		return nil
	}
	var best *StructureNode
	bestOverlap := 0
	for i := range children {
		summary := ""
		if children[i].Summary != nil {
			summary = *children[i].Summary
		}
		candidateTokens := structuralTokenize(children[i].Title + " " + summary)
		overlap := 0
		for t := range qTokens {
			if _, ok := candidateTokens[t]; ok {
				overlap++
			}
		}
		if overlap > bestOverlap {
			bestOverlap = overlap
			best = &children[i]
		}
	}
	return best
}

// StructuralNavigator is structural table-of-contents navigation over a
// Client. Every fetch (FetchRootNode/FetchChildNodes/the final ranked
// RAG_RETRIEVE) goes through Client.Query — the same governed /query
// surface — so ACL/cell-masking apply identically at every level; there is
// no bypass path.
type StructuralNavigator struct {
	c *Client
}

// NewStructuralNavigator constructs a StructuralNavigator that inherits the
// parent client's auth/tenant context.
func NewStructuralNavigator(c *Client) *StructuralNavigator {
	return &StructuralNavigator{c: c}
}

func structuralQueryOpts(purpose string) []QueryOption {
	if purpose == "" {
		return nil
	}
	return []QueryOption{WithPurpose(purpose)}
}

// FetchChildNodesOptions configures StructuralNavigator.FetchChildNodes.
type FetchChildNodesOptions struct {
	ReportID string
	// ParentID is nil to fetch the synthetic tree root's single row.
	ParentID *string
	// Purpose falls back to the parent client's DefaultPurpose when empty.
	Purpose string
}

// FetchChildNodes fetches the direct children of opts.ParentID (or the
// synthetic root's single row when opts.ParentID is nil) for
// opts.ReportID's DocumentStructureNode tree, via a plain governed SELECT —
// the same /query surface /rag/query itself delegates to server-side, so
// ACL/cell-masking apply identically.
func (n *StructuralNavigator) FetchChildNodes(
	ctx context.Context,
	opts FetchChildNodesOptions,
) ([]StructureNode, error) {
	var parentClause string
	if opts.ParentID == nil {
		parentClause = "parent_id IS NULL"
	} else {
		parentClause = "parent_id = " + sqlLiteral(*opts.ParentID)
	}
	sql := fmt.Sprintf(
		"SELECT * FROM DocumentStructureNode WHERE report_id = %s AND %s",
		sqlLiteral(opts.ReportID), parentClause,
	)
	result, err := n.c.Query(ctx, sql, structuralQueryOpts(opts.Purpose)...)
	if err != nil {
		return nil, err
	}
	nodes := make([]StructureNode, 0, len(result.Rows))
	for _, row := range result.Rows {
		nodes = append(nodes, StructureNodeFromRow(row))
	}
	return nodes, nil
}

// FetchRootNodeOptions configures StructuralNavigator.FetchRootNode.
type FetchRootNodeOptions struct {
	ReportID string
	// Purpose falls back to the parent client's DefaultPurpose when empty.
	Purpose string
}

// FetchRootNode fetches opts.ReportID's synthetic tree root (parent_id IS
// NULL), or nil when the document has no DocumentStructureNode tree at all
// — e.g. it was ingested with no section_path anywhere
// (document_structure_batch returns no batch in that case).
func (n *StructuralNavigator) FetchRootNode(
	ctx context.Context,
	opts FetchRootNodeOptions,
) (*StructureNode, error) {
	roots, err := n.FetchChildNodes(ctx, FetchChildNodesOptions{
		ReportID: opts.ReportID,
		ParentID: nil,
		Purpose:  opts.Purpose,
	})
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, nil
	}
	return &roots[0], nil
}

// structuralRowToHit translates one raw RAG_RETRIEVE result row into a
// RagHit, mirroring the server's own translate_hit
// (crates/relata-cli/src/serve/query/rag_query.rs) field-for-field:
// text_body -> Text, canonical_entity_id -> EntityIDs (normalised to a
// slice; the stored field accepts either a bare string or an array).
func structuralRowToHit(row map[string]any, rerankRequested bool) RagHit {
	var entityIDs []string
	switch raw := row["canonical_entity_id"].(type) {
	case []any:
		entityIDs = make([]string, 0, len(raw))
		for _, v := range raw {
			entityIDs = append(entityIDs, fmt.Sprint(v))
		}
	case string:
		if raw != "" {
			entityIDs = []string{raw}
		}
	}
	var rerankScore *float64
	if rerankRequested {
		if v, ok := row["_rerank_score"]; ok && v != nil {
			f := structuralFloatFromAny(v)
			rerankScore = &f
		}
	}
	var sectionPath []string
	if raw, ok := row["section_path"].([]any); ok {
		sectionPath = make([]string, 0, len(raw))
		for _, v := range raw {
			sectionPath = append(sectionPath, fmt.Sprint(v))
		}
	}
	return RagHit{
		BM25Score:   structuralFloatFromAny(row["_bm25_score"]),
		VectorScore: structuralFloatFromAny(row["_vector_score"]),
		RerankScore: rerankScore,
		ChunkID:     structuralRowString(row["chunk_id"]),
		ReportID:    structuralRowString(row["report_id"]),
		Text:        structuralRowString(row["text_body"]),
		SectionPath: sectionPath,
		PageStart:   intFromAny(row["page_start"]),
		PageEnd:     intFromAny(row["page_end"]),
		PrevChunkID: structuralRowStringPtr(row["prev_chunk_id"]),
		NextChunkID: structuralRowStringPtr(row["next_chunk_id"]),
		EntityIDs:   entityIDs,
	}
}

// structuralRankedLeafChunksOptions configures
// StructuralNavigator.fetchRankedLeafChunks.
type structuralRankedLeafChunksOptions struct {
	Type     string
	Question string
	ChunkIDs []string
	Purpose  string
	TopK     int
	Rerank   bool
}

// fetchRankedLeafChunks ranks opts.ChunkIDs (a leaf node's own
// LeafChunkIDs) against opts.Question via a governed RAG_RETRIEVE,
// restricted with `WHERE chunk_id IN (...)` — same grammar/ordering as the
// server's search_mode: structural second-hop query.
func (n *StructuralNavigator) fetchRankedLeafChunks(
	ctx context.Context,
	opts structuralRankedLeafChunksOptions,
) (*RagQueryResponse, error) {
	if err := validateIdentifier(opts.Type, "type"); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(opts.ChunkIDs))
	for _, id := range opts.ChunkIDs {
		ids = append(ids, sqlLiteral(id))
	}
	sql := fmt.Sprintf(
		"PURPOSE %s RAG_RETRIEVE FROM %s QUERY %s TOP_K %d",
		sqlLiteral(opts.Purpose), opts.Type, sqlLiteral(opts.Question), opts.TopK,
	)
	if opts.Rerank {
		sql += " RERANK"
	}
	sql += " WEIGHTS 0.33 0.34 0.33"
	sql += fmt.Sprintf(" WHERE chunk_id IN (%s)", strings.Join(ids, ", "))

	result, err := n.c.Query(ctx, sql, WithPurpose(opts.Purpose))
	if err != nil {
		return nil, err
	}
	hits := make([]RagHit, 0, len(result.Rows))
	for _, row := range result.Rows {
		hits = append(hits, structuralRowToHit(row, opts.Rerank))
	}
	return &RagQueryResponse{Hits: hits}, nil
}

// NavigateStructuralTreeOptions configures
// StructuralNavigator.NavigateStructuralTree.
type NavigateStructuralTreeOptions struct {
	ReportID string
	Question string
	// Type is the object type to rank the leaf's LeafChunkIDs against
	// (e.g. "DocumentChunk").
	Type string
	// SelectChild defaults to LexicalChildSelector when nil.
	SelectChild ChildSelector
	// Purpose falls back to the parent client's DefaultPurpose when empty.
	Purpose string
	// TopK defaults to RagDefaultTopK when 0.
	TopK   int
	Rerank bool
	// MaxDepth defaults to DefaultMaxDepth when 0.
	MaxDepth int
}

// NavigateStructuralTree agentically navigates opts.ReportID's
// DocumentStructureNode tree (#4542): descends from the synthetic root, one
// level per FetchChildNodes call, letting opts.SelectChild (or
// LexicalChildSelector, its default) reason over each level's candidate
// titles/summaries — until a level has no children (a leaf) or
// SelectChild returns nil (stop here) — then ranks that node's own
// LeafChunkIDs against opts.Question.
//
// This is the true multi-hop agent loop #4542's design describes,
// complementing (not replacing) /rag/query's single-shot search_mode:
// structural (#4514) — SelectChild is where an application plugs in real
// reasoning (e.g. an LLM call over the candidates' Summary fields);
// LexicalChildSelector is the LLM-free default.
//
// Every fetch (FetchRootNode/FetchChildNodes/the final ranked
// RAG_RETRIEVE) goes through Client.Query — the same governed /query
// surface — so ACL/cell-masking apply identically at every level; there is
// no bypass path.
//
// Returns an empty RagQueryResponse when opts.ReportID has no
// DocumentStructureNode tree at all, or the node reached has no
// LeafChunkIDs — never errors for "nothing to navigate," matching every
// other search_mode's "genuinely nothing matched" shape.
//
// Returns ErrPurposeRequired when neither opts.Purpose nor the client's
// DefaultPurpose is set.
func (n *StructuralNavigator) NavigateStructuralTree(
	ctx context.Context,
	opts NavigateStructuralTreeOptions,
) (*RagQueryResponse, error) {
	purpose := opts.Purpose
	if purpose == "" {
		purpose = n.c.defaultPurpose
	}
	if purpose == "" {
		return nil, ErrPurposeRequired
	}

	current, err := n.FetchRootNode(ctx, FetchRootNodeOptions{ReportID: opts.ReportID, Purpose: purpose})
	if err != nil {
		return nil, err
	}
	if current == nil {
		return &RagQueryResponse{Hits: []RagHit{}}, nil
	}

	selectChild := opts.SelectChild
	if selectChild == nil {
		selectChild = LexicalChildSelector
	}
	maxDepth := opts.MaxDepth
	if maxDepth == 0 {
		maxDepth = DefaultMaxDepth
	}

	for depth := 0; depth < maxDepth; depth++ {
		children, ferr := n.FetchChildNodes(ctx, FetchChildNodesOptions{
			ReportID: opts.ReportID,
			ParentID: &current.NodeID,
			Purpose:  purpose,
		})
		if ferr != nil {
			return nil, ferr
		}
		if len(children) == 0 {
			break
		}
		chosen := selectChild(opts.Question, children)
		if chosen == nil {
			break
		}
		current = chosen
	}

	if len(current.LeafChunkIDs) == 0 {
		return &RagQueryResponse{Hits: []RagHit{}}, nil
	}

	topK := opts.TopK
	if topK == 0 {
		topK = RagDefaultTopK
	}

	return n.fetchRankedLeafChunks(ctx, structuralRankedLeafChunksOptions{
		Type:     opts.Type,
		Question: opts.Question,
		ChunkIDs: current.LeafChunkIDs,
		Purpose:  purpose,
		TopK:     topK,
		Rerank:   opts.Rerank,
	})
}
