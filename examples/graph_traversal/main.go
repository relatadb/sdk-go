// Command graph_traversal demonstrates the PATHS_BETWEEN graph operator via the
// Relata Go SDK.
//
// Scenario: A counter-terrorism analyst needs to map the network between two
// known entities — a suspected financier (entity-A) and a procurement
// front-company (entity-B). The CSR graph engine finds all paths within N hops,
// and the analyst then walks the path nodes to retrieve associated intelligence.
//
// Graph operators used:
//   - PATHS_BETWEEN(a, b, max_hops => N)   — Pregel BFS on the CSR graph
//   - LOOKUP_IDENTITY(id)                   — resolve a node to a full entity
//
// Usage:
//
//	go run ./examples/graph_traversal \
//	    -url    http://localhost:9090     \
//	    -token  $RELATA_BEARER_TOKEN      \
//	    -from   "entity-financier-001"   \
//	    -to     "entity-frontco-009"     \
//	    -hops   4
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/OpenWorkBench-Co/RelataDB/sdks/go/relata"
)

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token")
	fromID := flag.String("from", "entity-financier-001", "Source entity ID")
	toID := flag.String("to", "entity-frontco-009", "Target entity ID")
	maxHops := flag.Int("hops", 4, "Maximum traversal depth (1–8 recommended)")
	asOf := flag.String("as-of", "", "Bi-temporal snapshot date (empty = current)")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "investigation",
		// Graph traversal is CPU-intensive; allow more time for deep hops.
		Timeout: 2 * time.Minute,
	})

	ctx := context.Background()

	// ── Step 1: PATHS_BETWEEN ─────────────────────────────────────────────────
	// PathsBetween is a QueryBuilder convenience constructor. It emits:
	//   SELECT * FROM PATHS_BETWEEN('<from>', '<to>', max_hops => <n>)
	// Optional AS OF and WITH PROVENANCE clauses are appended by the builder.
	fmt.Printf("=== PATHS_BETWEEN('%s', '%s', max_hops => %d) ===\n",
		*fromID, *toID, *maxHops)

	pathBuilder := relata.PathsBetween(*fromID, *toID, *maxHops).
		Purpose("investigation").
		WithProvenance()

	if *asOf != "" {
		pathBuilder = pathBuilder.AsOf(*asOf)
	}

	fmt.Printf("SQL: %s\n\n", pathBuilder.SQL())

	pathResult, err := pathBuilder.Execute(ctx, client)
	if err != nil {
		if errors.Is(err, relata.ErrForbidden) {
			log.Fatal("access denied: caller lacks GRAPH_TRAVERSE permission")
		}
		log.Fatalf("PATHS_BETWEEN failed: %v", err)
	}

	fmt.Printf("Paths found: %d  elapsed: %d ms  query_id: %s\n\n",
		pathResult.RowCount, pathResult.ElapsedMs, pathResult.QueryID)

	if pathResult.RowCount == 0 {
		fmt.Printf("No paths found between %s and %s within %d hops.\n",
			*fromID, *toID, *maxHops)
		return
	}

	// Each row is a path. Print the hop sequence for each.
	pathResult.Each(func(row map[string]any) {
		pathID := row["path_id"]
		hops := row["hop_count"]
		nodes := row["node_ids"]   // typically a JSON array of entity IDs
		edges := row["edge_types"] // types of relationships along the path
		fmt.Printf("  path_id=%-20v  hops=%v  nodes=%v  edge_types=%v\n",
			pathID, hops, nodes, edges)
	})

	// ── Step 2: Resolve each unique node in the shortest path ─────────────────
	// The first path row is (by convention) the shortest. Extract its nodes
	// and resolve each through LOOKUP_IDENTITY.
	fmt.Printf("\n=== Step 2: Resolve nodes on the shortest path ===\n")

	firstPath := pathResult.Rows[0]
	nodeIDs := toStringSlice(firstPath["node_ids"])

	for i, nodeID := range nodeIDs {
		idResult, err := relata.LookupIdentity(nodeID).
			Purpose("investigation").
			Execute(ctx, client)
		if err != nil {
			fmt.Printf("  hop %d — could not resolve %s: %v\n", i, nodeID, err)
			continue
		}
		if idResult.RowCount == 0 {
			fmt.Printf("  hop %d — %s (no identity records)\n", i, nodeID)
			continue
		}
		row := idResult.Rows[0]
		fmt.Printf("  hop %-2d  entity_id=%-36s  name=%v  type=%v\n",
			i, nodeID, row["display_name"], row["entity_type"])
	}

	// ── Step 3: Relationship summary per edge type ────────────────────────────
	// Count distinct edge types across all returned paths to reveal the
	// dominant relationship class in the network.
	fmt.Printf("\n=== Step 3: Edge type distribution across all paths ===\n")

	edgeCounts := map[string]int{}
	pathResult.Each(func(row map[string]any) {
		for _, et := range toStringSlice(row["edge_types"]) {
			edgeCounts[et]++
		}
	})

	for edgeType, count := range edgeCounts {
		fmt.Printf("  %-30s  %d paths\n", edgeType, count)
	}

	// ── Step 4: Fetch PhoneCall links between the two endpoint entities ────────
	// PATHS_BETWEEN tells you a path exists; a direct predicate query then
	// retrieves the actual evidence records along the most interesting edges.
	fmt.Printf("\n=== Step 4: Direct PhoneCall evidence between endpoints ===\n")

	callResult, err := relata.NewQuery("SELECT * FROM PhoneCall").
		Purpose("investigation").
		Where(fmt.Sprintf(
			"(caller_entity_id = '%s' AND callee_entity_id = '%s') OR "+
				"(caller_entity_id = '%s' AND callee_entity_id = '%s')",
			*fromID, *toID, *toID, *fromID,
		)).
		Limit(20).
		Execute(ctx, client)
	if err != nil {
		log.Fatalf("call query failed: %v", err)
	}

	fmt.Printf("Direct calls: %d\n", callResult.RowCount)
	callResult.Each(func(row map[string]any) {
		fmt.Printf("  %v → %v  duration_s=%v  timestamp=%v\n",
			row["caller_msisdn"], row["callee_msisdn"],
			row["duration_s"], row["call_timestamp"])
	})

	// ── Summary ───────────────────────────────────────────────────────────────
	fmt.Printf("\n=== Graph traversal summary ===\n")
	fmt.Printf("From        : %s\n", *fromID)
	fmt.Printf("To          : %s\n", *toID)
	fmt.Printf("Max hops    : %d\n", *maxHops)
	fmt.Printf("Paths found : %d\n", pathResult.RowCount)
	fmt.Printf("Direct calls: %d\n", callResult.RowCount)
}

// toStringSlice coerces a JSON-decoded value ([]any or string) to []string.
// PATHS_BETWEEN returns node_ids and edge_types as JSON arrays; the Go JSON
// decoder maps them to []any containing string elements.
func toStringSlice(v any) []string {
	switch val := v.(type) {
	case []any:
		out := make([]string, 0, len(val))
		for _, elem := range val {
			if s, ok := elem.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return val
	case string:
		if val == "" {
			return nil
		}
		return []string{val}
	default:
		return nil
	}
}
