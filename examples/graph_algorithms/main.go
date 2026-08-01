// Command graph_algorithms demonstrates the graph operator chain for
// fraud-ring + centrality analysis (#967).
//
// Operators covered:
//   - GraphDijkstra        — shortest path between two entities
//   - GraphPageRank        — centrality (who are the hub entities?)
//   - GraphSCC             — strongly connected components (fraud-ring)
//   - GraphCycles          — cyclic flow (e.g. money-laundering loops)
//   - GraphTriangleCount   — triadic closure (co-offending networks)
//
// Each operator translates to a governed SQL TVF call (POST /query) with the
// caller's purpose recorded in the audit log.
//
// Usage:
//
//	go run ./examples/graph_algorithms \
//	    -url http://localhost:9090 -token $RELATA_TOKEN \
//	    -from person-001 -to person-002
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

const objectType = "Person"

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (optional)")
	fromID := flag.String("from", "person-001", "Source entity ID for GRAPH_DIJKSTRA")
	toID := flag.String("to", "person-002", "Destination entity ID for GRAPH_DIJKSTRA")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "investigation",
		Timeout:        60 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// ── 1. Shortest path ──────────────────────────────────────────────────
	fmt.Printf("=== 1. GRAPH_DIJKSTRA: %s → %s ===\n", *fromID, *toID)
	path, err := client.GraphDijkstra(ctx, "investigation", objectType, *fromID, *toID)
	printRows("paths", path, err, []string{"hop", "node_id", "edge_type", "weight"})

	// ── 2. Centrality ─────────────────────────────────────────────────────
	fmt.Println("\n=== 2. GRAPH_PAGERANK: centrality (damping=0.85, 20 iter) ===")
	centrality, err := client.GraphPageRank(ctx, "investigation", objectType, &relata.GraphPageRankOptions{Damping: 0.85, MaxIter: 20})
	printRows("centrality", centrality, err, []string{"node_id", "score"})

	// ── 3. SCC (fraud rings) ───────────────────────────────────────────────
	fmt.Println("\n=== 3. GRAPH_SCC: fraud-ring candidates (≥3-node SCCs) ===")
	scc, err := client.GraphSCC(ctx, "investigation", objectType)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		scc = map[string]any{}
	}
	sccRows, _ := scc["rows"].([]any)
	big := 0
	for _, r := range sccRows {
		row, _ := r.(map[string]any)
		if s, ok := row["size"].(float64); ok && s >= 3 {
			big++
		}
	}
	fmt.Printf("  %d SCC(s) total, %d with size ≥ 3\n", len(sccRows), big)

	// ── 4. Cycles ─────────────────────────────────────────────────────────
	fmt.Println("\n=== 4. GRAPH_CYCLES: cyclic flows ===")
	cycles, err := client.GraphCycles(ctx, "investigation", objectType)
	printRows("cycles", cycles, err, []string{"cycle_id", "length", "nodes"})

	// ── 5. Triangles ──────────────────────────────────────────────────────
	fmt.Println("\n=== 5. GRAPH_TRIANGLE_COUNT: triadic closure ===")
	triangles, err := client.GraphTriangleCount(ctx, "investigation", objectType)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		count := int64(0)
		if c, ok := triangles["count"].(float64); ok {
			count = int64(c)
		} else if rows, _ := triangles["rows"].([]any); len(rows) > 0 {
			if row, _ := rows[0].(map[string]any); row != nil {
				if c, ok := row["count"].(float64); ok {
					count = int64(c)
				}
			}
		}
		fmt.Printf("  %d triangle(s) found\n", count)
	}
}

// printRows renders up to 10 rows from a query-shaped response.
func printRows(label string, resp map[string]any, err error, fields []string) {
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	rows, _ := resp["rows"].([]any)
	if len(rows) == 0 {
		fmt.Printf("  (empty result — %v)\n", resp)
		return
	}
	shown := 10
	if len(rows) < shown {
		shown = len(rows)
	}
	fmt.Printf("  %s: %d row(s), showing first %d:\n", label, len(rows), shown)
	for _, r := range rows[:shown] {
		row, _ := r.(map[string]any)
		line := ""
		for _, f := range fields {
			line += fmt.Sprintf("%s=%v  ", f, row[f])
		}
		fmt.Printf("    %s\n", line)
	}
}
