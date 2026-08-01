// Command multi_search demonstrates federated multi-query search (#967).
//
// POST /multi-search lets you query N types (or N query strings) in a single
// HTTP call. The server runs each query in parallel and returns a combined
// result envelope with processing_time_ms for the whole batch.
//
// Usage:
//
//	go run ./examples/multi_search -url http://localhost:9090 -token $RELATA_TOKEN
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
	needle := flag.String("needle", "alice", "search needle")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "analytics",
		Timeout:        30 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Fan out across 3 types in one call ─────────────────────────────
	fmt.Printf("=== 1. Multi-search across 3 types for '%s' ===\n", *needle)
	resp, err := client.MultiSearch(ctx, []map[string]any{
		{"query": *needle, "type": "Person", "limit": 5},
		{"query": *needle, "type": "EmailAddress", "limit": 5},
		{"query": *needle, "type": "PhoneCall", "limit": 5},
	})
	printMulti(ctx, resp, err)

	// ── 2. Same type, different query strings ─────────────────────────────
	fmt.Println("\n=== 2. Multi-search: same type, different queries ===")
	resp, err = client.MultiSearch(ctx, []map[string]any{
		{"query": "alice", "type": "Person", "limit": 3},
		{"query": "bob", "type": "Person", "limit": 3},
		{"query": "carol", "type": "Person", "limit": 3},
	})
	printMulti(ctx, resp, err)

	// ── 3. With matching_strategy + typo_tolerance ────────────────────────
	fmt.Println("\n=== 3. With matching_strategy='all' ===")
	resp, err = client.MultiSearch(ctx, []map[string]any{
		{
			"query":            *needle,
			"type":             "Person",
			"limit":            5,
			"matching_strategy": "all",
		},
	})
	printMulti(ctx, resp, err)
}

func printMulti(_ context.Context, resp map[string]any, err error) {
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	results, _ := resp["results"].([]any)
	elapsed := resp["processing_time_ms"]
	fmt.Printf("  %d sub-query result set(s) in %v ms\n", len(results), elapsed)
	for i, r := range results {
		sub, _ := r.(map[string]any)
		hits, _ := sub["hits"].([]any)
		total := sub["estimatedTotalHits"]
		if total == nil {
			total = sub["total"]
		}
		fmt.Printf("    [%d] hits returned=%d, estimated total=%v\n", i, len(hits), total)
	}
}
