// Command hybrid_search demonstrates BM25 + vector search via
// VectorClient.HybridSearch and VectorClient.KNNSearch (#2678).
//
// HYBRID_SEARCH is the operator that makes Relata more than "yet another
// vector DB" or "yet another BM25 engine": the server embeds queryText
// itself and fuses the BM25 + vector rankings via reciprocal rank fusion
// (ADR-175) — the /query SQL surface has no caller-supplied-embedding
// grammar, so HybridSearch only accepts queryText (see HybridSearch's doc
// comment in relata/vectors.go).
//
// This walkthrough embeds a few short texts (POST /embed, #1172), ingests
// them into a Document type with the embedding pre-computed in the
// _emb_text slot (the caller-supplied convention, see
// docs/src/end-users/search.md), then runs three searches side by side:
// BM25-only via HybridSearch, vector-only via KNNSearch against the
// pre-computed _emb_text slot, and BM25+vector fusion via HybridSearch with
// RERANK + METRIC options.
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

	// ── 2. BM25-only leg via HybridSearch ────────────────────────────────────
	fmt.Println("\n=== 2. BM25-only: HybridSearch queryText=\"graph retrieval\" ===")
	hits, err := vectors.HybridSearch(
		ctx, "Document", "graph retrieval", &relata.HybridSearchOptions{K: 5},
	)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	printHits(hits)

	// ── 3. Vector-only leg via KNNSearch against the pre-computed slot ───────
	fmt.Println("\n=== 3. Vector-only: KNNSearch nearest neighbour to doc-2's embedding ===")
	probe, err := vectors.Embed(ctx, "approximate nearest neighbour search", "")
	if err != nil {
		fmt.Printf("  embed error: %v\n", err)
		return
	}
	hits, err = vectors.KNNSearch(ctx, "Document", "_emb_text", probe.Embedding, 5, nil)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	printHits(hits)

	// ── 4. Fused: BM25 + vector via HybridSearch's RERANK/METRIC options ────
	// (ADR-175). The server, not the caller, supplies the vector leg's
	// embedding — HybridSearch has no caller-supplied-embedding grammar.
	fmt.Println("\n=== 4. Fused: HybridSearch with RERANK + METRIC ===")
	hits, err = vectors.HybridSearch(
		ctx, "Document", "graph retrieval",
		&relata.HybridSearchOptions{K: 5, Rerank: true, Metric: "cosine"},
	)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	printHits(hits)
}
