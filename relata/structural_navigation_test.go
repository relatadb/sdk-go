package relata

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests for structural table-of-contents navigation (#4542), TS/Go parity
// port of sdks/python/tests/test_structural_navigation.py (#4581).

// queuedNavServer returns one JSON response body (as {"data": rows, ...})
// per POST /query call, in order. Extra calls beyond len(responses) get an
// empty row set. sentSQL captures every request's "sql" field in order.
func queuedNavServer(responses [][]map[string]any) (*httptest.Server, *[]string) {
	sentSQL := make([]string, 0, len(responses))
	idx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		if sql, ok := body["sql"].(string); ok {
			sentSQL = append(sentSQL, sql)
		}
		var rows []map[string]any
		if idx < len(responses) {
			rows = responses[idx]
		}
		idx++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": rows, "query_id": "qid-1", "elapsed_ms": 1,
		})
	}))
	return srv, &sentSQL
}

func strPtr(s string) *string { return &s }

var navRootRow = map[string]any{
	"node_id": "doc_1::", "report_id": "doc_1", "parent_id": nil,
	"title": "filing.pdf", "depth": 0.0, "page_start": 1.0, "page_end": 10.0,
	"summary": nil, "child_count": 1.0, "leaf_chunk_ids": "[]",
}

var navPart1Row = map[string]any{
	"node_id": "doc_1::Part I", "report_id": "doc_1", "parent_id": "doc_1::",
	"title": "Part I", "depth": 1.0, "page_start": 1.0, "page_end": 4.0,
	"summary": "Liquidity and termination clauses.", "child_count": 0.0,
	"leaf_chunk_ids": `["ch_1", "ch_2"]`,
}

var navUnrelatedRow = map[string]any{
	"node_id": "doc_1::Part II", "report_id": "doc_1", "parent_id": "doc_1::",
	"title": "Part II", "depth": 1.0, "page_start": 5.0, "page_end": 8.0,
	"summary": "Renewal options.", "child_count": 0.0,
	"leaf_chunk_ids": `["ch_3"]`,
}

var navLeafChunkHitRow = map[string]any{
	"chunk_id": "ch_1", "report_id": "doc_1",
	"text_body":    "Liquidity terms are defined here.",
	"section_path": []any{"Part I"},
	"page_start":   1.0, "page_end": 1.0,
	"prev_chunk_id": nil, "next_chunk_id": "ch_2",
	"canonical_entity_id": "e_1",
	"_bm25_score":         2.1, "_vector_score": 0.9,
}

// ── StructureNodeFromRow ─────────────────────────────────────────────────

func TestStructureNodeFromRow_ParsesLeafChunkIDsJSONString(t *testing.T) {
	node := StructureNodeFromRow(navPart1Row)
	if node.NodeID != "doc_1::Part I" {
		t.Fatalf("NodeID = %q", node.NodeID)
	}
	if node.ParentID == nil || *node.ParentID != "doc_1::" {
		t.Fatalf("ParentID = %v", node.ParentID)
	}
	if node.Title != "Part I" {
		t.Fatalf("Title = %q", node.Title)
	}
	if got := strings.Join(node.LeafChunkIDs, ","); got != "ch_1,ch_2" {
		t.Fatalf("LeafChunkIDs = %v", node.LeafChunkIDs)
	}
	if !node.IsLeaf() {
		t.Fatalf("expected IsLeaf() true (child_count == 0)")
	}
}

func TestStructureNodeFromRow_HandlesMissingAndMalformedLeafIDs(t *testing.T) {
	row := map[string]any{}
	for k, v := range navRootRow {
		row[k] = v
	}
	row["leaf_chunk_ids"] = "not json"
	node := StructureNodeFromRow(row)
	if len(node.LeafChunkIDs) != 0 {
		t.Fatalf("LeafChunkIDs = %v, want empty", node.LeafChunkIDs)
	}
	if node.IsLeaf() {
		t.Fatalf("expected IsLeaf() false (child_count == 1)")
	}
}

// ── FetchRootNode / FetchChildNodes ─────────────────────────────────────

func TestFetchRootNode_QueriesParentIDIsNull(t *testing.T) {
	srv, sentSQL := queuedNavServer([][]map[string]any{{navRootRow}})
	defer srv.Close()
	nav := NewStructuralNavigator(newTestClient(srv, &ClientOptions{}))

	node, err := nav.FetchRootNode(context.Background(), FetchRootNodeOptions{
		ReportID: "doc_1", Purpose: "research",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node == nil || node.NodeID != "doc_1::" {
		t.Fatalf("node = %v", node)
	}
	if !strings.Contains((*sentSQL)[0], "parent_id IS NULL") {
		t.Fatalf("sql = %q", (*sentSQL)[0])
	}
	if !strings.Contains((*sentSQL)[0], "report_id = 'doc_1'") {
		t.Fatalf("sql = %q", (*sentSQL)[0])
	}
}

func TestFetchRootNode_ReturnsNilWhenNoTree(t *testing.T) {
	srv, _ := queuedNavServer([][]map[string]any{{}})
	defer srv.Close()
	nav := NewStructuralNavigator(newTestClient(srv, &ClientOptions{}))

	node, err := nav.FetchRootNode(context.Background(), FetchRootNodeOptions{
		ReportID: "doc_missing", Purpose: "research",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node != nil {
		t.Fatalf("node = %v, want nil", node)
	}
}

func TestFetchChildNodes_ScopesByParentID(t *testing.T) {
	srv, sentSQL := queuedNavServer([][]map[string]any{{navPart1Row, navUnrelatedRow}})
	defer srv.Close()
	nav := NewStructuralNavigator(newTestClient(srv, &ClientOptions{}))

	children, err := nav.FetchChildNodes(context.Background(), FetchChildNodesOptions{
		ReportID: "doc_1", ParentID: strPtr("doc_1::"), Purpose: "research",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(children) != 2 || children[0].Title != "Part I" || children[1].Title != "Part II" {
		t.Fatalf("children = %+v", children)
	}
	if !strings.Contains((*sentSQL)[0], "parent_id = 'doc_1::'") {
		t.Fatalf("sql = %q", (*sentSQL)[0])
	}
}

// ── LexicalChildSelector ─────────────────────────────────────────────────

func TestLexicalChildSelector_PicksBestOverlap(t *testing.T) {
	children := []StructureNode{StructureNodeFromRow(navPart1Row), StructureNodeFromRow(navUnrelatedRow)}
	chosen := LexicalChildSelector("What are the liquidity termination clauses?", children)
	if chosen == nil || chosen.Title != "Part I" {
		t.Fatalf("chosen = %v", chosen)
	}
}

func TestLexicalChildSelector_ReturnsNilOnNoOverlap(t *testing.T) {
	children := []StructureNode{StructureNodeFromRow(navPart1Row), StructureNodeFromRow(navUnrelatedRow)}
	if chosen := LexicalChildSelector("zzz qqq nonsense", children); chosen != nil {
		t.Fatalf("chosen = %v, want nil", chosen)
	}
}

// ── NavigateStructuralTree — the multi-hop descent ──────────────────────

func TestNavigateStructuralTree_DescendsAndRanksLeafChunks(t *testing.T) {
	srv, sentSQL := queuedNavServer([][]map[string]any{
		{navRootRow},                   // 1. root
		{navPart1Row, navUnrelatedRow}, // 2. root's children
		{},                             // 3. Part I's children — none, it's a leaf
		{navLeafChunkHitRow},           // 4. ranked RAG_RETRIEVE over ch_1/ch_2
	})
	defer srv.Close()
	nav := NewStructuralNavigator(newTestClient(srv, &ClientOptions{}))

	resp, err := nav.NavigateStructuralTree(context.Background(), NavigateStructuralTreeOptions{
		ReportID: "doc_1",
		Question: "What are the liquidity termination clauses?",
		Type:     "DocumentChunk",
		Purpose:  "research",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*sentSQL) != 4 {
		t.Fatalf("sentSQL = %v", *sentSQL)
	}
	if !strings.Contains((*sentSQL)[0], "parent_id IS NULL") {
		t.Fatalf("sql[0] = %q", (*sentSQL)[0])
	}
	if !strings.Contains((*sentSQL)[1], "parent_id = 'doc_1::'") {
		t.Fatalf("sql[1] = %q", (*sentSQL)[1])
	}
	if !strings.Contains((*sentSQL)[2], "parent_id = 'doc_1::Part I'") {
		t.Fatalf("sql[2] = %q", (*sentSQL)[2])
	}
	if !strings.Contains((*sentSQL)[3], "RAG_RETRIEVE FROM DocumentChunk") {
		t.Fatalf("sql[3] = %q", (*sentSQL)[3])
	}
	if !strings.Contains((*sentSQL)[3], "chunk_id IN ('ch_1', 'ch_2')") {
		t.Fatalf("sql[3] = %q", (*sentSQL)[3])
	}

	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %+v", resp.Hits)
	}
	hit := resp.Hits[0]
	if hit.ChunkID != "ch_1" {
		t.Fatalf("ChunkID = %q", hit.ChunkID)
	}
	if hit.Text != "Liquidity terms are defined here." {
		t.Fatalf("Text = %q", hit.Text)
	}
	if hit.BM25Score != 2.1 || hit.VectorScore != 0.9 {
		t.Fatalf("scores = %v/%v", hit.BM25Score, hit.VectorScore)
	}
	if len(hit.EntityIDs) != 1 || hit.EntityIDs[0] != "e_1" {
		t.Fatalf("EntityIDs = %v", hit.EntityIDs)
	}
	if hit.RerankScore != nil {
		t.Fatalf("RerankScore = %v, want nil", hit.RerankScore)
	}
}

func TestNavigateStructuralTree_ReturnsEmptyWhenNoTree(t *testing.T) {
	srv, sentSQL := queuedNavServer([][]map[string]any{{}})
	defer srv.Close()
	nav := NewStructuralNavigator(newTestClient(srv, &ClientOptions{}))

	resp, err := nav.NavigateStructuralTree(context.Background(), NavigateStructuralTreeOptions{
		ReportID: "doc_flat", Question: "anything", Type: "DocumentChunk", Purpose: "research",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Hits) != 0 {
		t.Fatalf("hits = %+v", resp.Hits)
	}
	if len(*sentSQL) != 1 {
		t.Fatalf("sentSQL = %v, want 1 call (stopped after root fetch found nothing)", *sentSQL)
	}
}

func TestNavigateStructuralTree_StopsWhenSelectorDeclines(t *testing.T) {
	// Root has children, but the (custom) selector never picks one —
	// descent stops at the root, and the root itself has no leaf_chunk_ids.
	srv, sentSQL := queuedNavServer([][]map[string]any{
		{navRootRow},
		{navPart1Row, navUnrelatedRow},
	})
	defer srv.Close()
	nav := NewStructuralNavigator(newTestClient(srv, &ClientOptions{}))

	resp, err := nav.NavigateStructuralTree(context.Background(), NavigateStructuralTreeOptions{
		ReportID: "doc_1",
		Question: "totally unrelated query text",
		Type:     "DocumentChunk",
		Purpose:  "research",
		SelectChild: func(_ string, _ []StructureNode) *StructureNode {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Hits) != 0 {
		t.Fatalf("hits = %+v", resp.Hits)
	}
	if len(*sentSQL) != 2 {
		t.Fatalf("sentSQL = %v, want 2 calls (root + one children fetch, no leaf-ranking call)", *sentSQL)
	}
}

func TestNavigateStructuralTree_RequiresPurpose(t *testing.T) {
	srv, _ := queuedNavServer([][]map[string]any{{navRootRow}})
	defer srv.Close()
	nav := NewStructuralNavigator(newTestClient(srv, &ClientOptions{}))

	_, err := nav.NavigateStructuralTree(context.Background(), NavigateStructuralTreeOptions{
		ReportID: "doc_1", Question: "q", Type: "DocumentChunk",
	})
	if !errors.Is(err, ErrPurposeRequired) {
		t.Fatalf("err = %v, want ErrPurposeRequired", err)
	}
}
