package relata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Tests for the typed /rag/query client — RAG epic foundational SDK ticket
// (#4523, wraps #4514's ADR-0299 contract). This is the thin pass-through
// mandated by ADR-0298 (canonical implementation lives in sdks/python).

func TestRagClient_Query_PostsFullContract(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":[{
			"bm25_score":4.2,"vector_score":0.83,"rerank_score":null,
			"chunk_id":"chunk-1","report_id":"doc-1",
			"text":"RelataDB fuses BM25 and vector retrieval natively.",
			"section_path":["3","3.2"],"page_start":5,"page_end":6,
			"prev_chunk_id":null,"next_chunk_id":"chunk-2",
			"entity_ids":["ent-1","ent-2"]
		}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	resp, err := rag.Query(context.Background(), RagQueryRequest{
		Query:         "graph retrieval",
		Type:          "DocumentChunk",
		TopK:          5,
		Rerank:        true,
		SearchMode:    "lexical",
		EmbeddingSlot: "summary",
		Filters:       []RagFilter{{Field: "status", Op: "eq", Value: "published"}},
		AsOf:          "2026-01-01T00:00:00Z",
		Purpose:       "research",
		ExpandWindow:  true,
		GraphHops:     2,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotPath != "/rag/query" {
		t.Fatalf("path = %q, want /rag/query", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotBody["query"] != "graph retrieval" {
		t.Fatalf("query = %v", gotBody["query"])
	}
	if gotBody["type"] != "DocumentChunk" {
		t.Fatalf("type = %v", gotBody["type"])
	}
	if gotBody["top_k"].(float64) != 5 {
		t.Fatalf("top_k = %v", gotBody["top_k"])
	}
	if gotBody["rerank"] != true {
		t.Fatalf("rerank = %v", gotBody["rerank"])
	}
	if gotBody["search_mode"] != "lexical" {
		t.Fatalf("search_mode = %v", gotBody["search_mode"])
	}
	if gotBody["embedding_slot"] != "summary" {
		t.Fatalf("embedding_slot = %v", gotBody["embedding_slot"])
	}
	if gotBody["as_of"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("as_of = %v", gotBody["as_of"])
	}
	if gotBody["purpose"] != "research" {
		t.Fatalf("purpose = %v", gotBody["purpose"])
	}
	if gotBody["expand_window"] != true {
		t.Fatalf("expand_window = %v", gotBody["expand_window"])
	}
	if gotBody["graph_hops"].(float64) != 2 {
		t.Fatalf("graph_hops = %v", gotBody["graph_hops"])
	}

	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(resp.Hits))
	}
	hit := resp.Hits[0]
	if hit.BM25Score != 4.2 {
		t.Fatalf("bm25_score = %v, want 4.2 (must never be fused with vector_score)", hit.BM25Score)
	}
	if hit.VectorScore != 0.83 {
		t.Fatalf("vector_score = %v, want 0.83 (must never be fused with bm25_score)", hit.VectorScore)
	}
	if hit.ChunkID != "chunk-1" || hit.ReportID != "doc-1" {
		t.Fatalf("chunk_id/report_id = %q/%q", hit.ChunkID, hit.ReportID)
	}
	if len(hit.SectionPath) != 2 || hit.SectionPath[0] != "3" {
		t.Fatalf("section_path = %v", hit.SectionPath)
	}
	if hit.NextChunkID == nil || *hit.NextChunkID != "chunk-2" {
		t.Fatalf("next_chunk_id = %v", hit.NextChunkID)
	}
	if len(hit.EntityIDs) != 2 {
		t.Fatalf("entity_ids = %v", hit.EntityIDs)
	}
}

func TestRagClient_Query_AppliesContractDefaults(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":[]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "research"})
	rag := NewRagClient(c)
	_, err := rag.Query(context.Background(), RagQueryRequest{Query: "q", Type: "DocumentChunk"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotBody["top_k"].(float64) != RagDefaultTopK {
		t.Fatalf("top_k default = %v, want %d (RAG_RETRIEVE_DEFAULT_TOP_K)", gotBody["top_k"], RagDefaultTopK)
	}
	if gotBody["search_mode"] != "hybrid" {
		t.Fatalf("search_mode default = %v, want hybrid", gotBody["search_mode"])
	}
	if gotBody["embedding_slot"] != "text" {
		t.Fatalf("embedding_slot default = %v, want text", gotBody["embedding_slot"])
	}
	if gotBody["purpose"] != "research" {
		t.Fatalf("purpose = %v, want fallback to client DefaultPurpose", gotBody["purpose"])
	}
}

func TestRagClient_Query_ErrPurposeRequiredWhenNoneAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called when purpose is missing")
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	_, err := rag.Query(context.Background(), RagQueryRequest{Query: "q", Type: "DocumentChunk"})
	if !errors.Is(err, ErrPurposeRequired) {
		t.Fatalf("err = %v, want ErrPurposeRequired", err)
	}
}
