// Command hybrid_search_quickstart demonstrates the flagship hybrid retrieval
// path (HYBRID_SEARCH FROM <type> QUERY '<text>' LIMIT <k>), fused via
// reciprocal rank fusion (graph + BM25 + vector, ADR-175). The server embeds
// the query text server-side; caller-supplied embeddings are not accepted by
// the /query SQL surface (no vector-literal grammar).
//
// Usage:
//
//	go run ./examples/hybrid_search_quickstart -url http://localhost:9090 -token $RELATA_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (optional)")
	flag.Parse()

	c := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "analytics",
	})
	vectors := relata.NewVectorClient(c)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. hybrid search — BM25 ⊕ vector fusion ───────────────────────────
	fmt.Println("=== 1. hybrid search — BM25 ⊕ vector fusion ===")
	hits, err := vectors.HybridSearch(ctx, "Document", "graph database", &relata.HybridSearchOptions{
		K: 5,
	})
	if err != nil {
		fmt.Printf("HybridSearch: %v\n", err)
		return
	}
	for i, h := range hits {
		fmt.Printf("  %d. %v\n", i+1, h["id"])
	}

	// ── 2. tuned — RERANK + METRIC + WEIGHTS [graph, bm25, vector] ────────
	fmt.Println("=== 2. tuned — RERANK + METRIC + WEIGHTS ===")
	w := [3]float64{0.3, 0.5, 0.2}
	tuned, err := vectors.HybridSearch(ctx, "Document", "graph database", &relata.HybridSearchOptions{
		K: 5, Rerank: true, Metric: "cosine", Weights: &w,
	})
	if err != nil {
		fmt.Printf("HybridSearch (tuned): %v\n", err)
		return
	}
	fmt.Printf("tuned hits: %d\n", len(tuned))
}
