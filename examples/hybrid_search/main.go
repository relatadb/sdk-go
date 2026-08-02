// Command hybrid_search demonstrates BM25 + vector fusion via
// VectorClient.HybridSearch (#2678).
//
// HYBRID_SEARCH is the operator that makes Relata more than "yet another
// vector DB" or "yet another BM25 engine": supply queryText (BM25 leg),
// queryEmbedding (vector leg), or both — when both are present the server
// fuses the two rankings via reciprocal rank fusion (ADR-175).
//
// This walkthrough embeds a few short texts (POST /embed, #1172), ingests
// them into a Document type with the embedding pre-computed in the
// _emb_text slot (the caller-supplied convention, see
// docs/src/end-users/search.md), then runs HybridSearch three ways —
// BM25-only, vector-only, and fused — so the effect of fusion is visible
// side by side.
//
// Usage:
//
//	go run ./examples/hybrid_search -url http://localhost:9090 -token $RELATA_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

type doc struct {
	id    string
	title string
	body  string
}

func printHits(hits []map[string]any) {
	for _, row := range hits {
		score, _ := row["_score"].(float64)
		fmt.Printf("  score=%.4f  %v\n", score, row["title"])
	}
}

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (optional)")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "rag",
		Timeout:        30 * time.Second,
	})
	ingest := relata.NewIngestClient(client)
	vectors := relata.NewVectorClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	docs := []doc{
		{
			id:    "doc-1",
			title: "Knowledge graphs 101",
			body:  "An introduction to entities, edges, and graph retrieval.",
		},
		{
			id:    "doc-2",
			title: "Vector search at scale",
			body:  "HNSW indexes and approximate nearest neighbour retrieval.",
		},
		{
			id:    "doc-3",
			title: "Bi-temporal databases",
			body:  "Tracking valid time and system time for auditable history.",
		},
	}

	// ── 1. Embed + ingest 3 Documents (schemaless via IngestClient.Bulk) ────
	fmt.Println("=== 1. Embed + ingest 3 Documents ===")
	rows := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		resp, err := vectors.Embed(ctx, d.body, "")
		if err != nil {
			fmt.Printf("  embed error: %v\n", err)
			return
		}
		rows = append(rows, map[string]any{
			"id":        d.id,
			"title":     d.title,
			"body":      d.body,
			"_emb_text": resp.Embedding,
		})
	}
	if _, err := ingest.Bulk(ctx, "Document", rows, nil); err != nil {
		fmt.Printf("  ingest error: %v\n", err)
		return
	}
	fmt.Printf("  wrote %d rows with pre-computed _emb_text embeddings\n", len(rows))

	// ── 2. BM25-only leg (queryText, no embedding) ───────────────────────────
	fmt.Println("\n=== 2. BM25-only: queryText=\"graph retrieval\" ===")
	hits, err := vectors.HybridSearch(
		ctx, "Document", "graph retrieval", nil, "", &relata.HybridSearchOptions{K: 5},
	)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	printHits(hits)

	// ── 3. Vector-only leg (queryEmbedding, no text) ─────────────────────────
	fmt.Println("\n=== 3. Vector-only: nearest neighbour to doc-2's embedding ===")
	probe, err := vectors.Embed(ctx, "approximate nearest neighbour search", "")
	if err != nil {
		fmt.Printf("  embed error: %v\n", err)
		return
	}
	hits, err = vectors.HybridSearch(
		ctx, "Document", "", probe.Embedding, "_emb_text", &relata.HybridSearchOptions{K: 5},
	)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	printHits(hits)

	// ── 4. Fused: both legs — reciprocal rank fusion (ADR-175) ───────────────
	fmt.Println("\n=== 4. Fused: queryText + queryEmbedding together ===")
	fused, err := vectors.Embed(ctx, "graph retrieval with vectors", "")
	if err != nil {
		fmt.Printf("  embed error: %v\n", err)
		return
	}
	hits, err = vectors.HybridSearch(
		ctx, "Document", "graph retrieval", fused.Embedding, "_emb_text",
		&relata.HybridSearchOptions{K: 5},
	)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	printHits(hits)
}
