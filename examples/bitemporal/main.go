// Command bitemporal demonstrates point-in-time travel + provenance
// (Phase 3, ADR-117).
//
// Relata is bi-temporal: every row carries four timestamps —
//   - valid_from / valid_to     — when the fact was true in the real world
//   - system_from / system_to   — when the database knew about it
//
// Two SQL clauses operate on these:
//
//   AS OF '<ts>'         — point-in-time snapshot (rows whose
//                          [valid_from, valid_to) interval contains <ts>
//                          AND whose [system_from, system_to) interval also
//                          contains <ts>).
//   WITH PROVENANCE      — adds PROV-O columns: source_id, collector, method,
//                          confidence, observed_at, recorded_at.
//
// Usage:
//
//	go run ./examples/bitemporal -url http://localhost:9090 -token $RELATA_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/relatadb/sdk-go/relata"
)

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (optional)")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "analytics",
		Timeout:        30 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Current snapshot (baseline) ────────────────────────────────────
	fmt.Println("=== 1. Current snapshot of Person ===")
	run(ctx, client, "SELECT _pk, name, valid_from, valid_to FROM Person LIMIT 5")

	// ── 2. AS OF '<ts>' — what was true at a past point in time? ──────────
	fmt.Println("\n=== 2. AS OF '2024-06-01' — point-in-time snapshot ===")
	run(ctx, client, "SELECT _pk, name FROM Person AS OF '2024-06-01' LIMIT 5")

	// ── 3. WITH PROVENANCE — PROV-O columns ───────────────────────────────
	fmt.Println("\n=== 3. WITH PROVENANCE — provenance columns ===")
	run(ctx, client, "SELECT _pk, name, source_id, collector, method, confidence FROM Person WITH PROVENANCE LIMIT 5")

	// ── 4. Combined: AS OF + WITH PROVENANCE ──────────────────────────────
	fmt.Println("\n=== 4. Combined: AS OF + WITH PROVENANCE — forensic snapshot ===")
	run(ctx, client,
		"SELECT _pk, name, source_id FROM Person AS OF '2024-06-01' WITH PROVENANCE LIMIT 5")

	// ── 5. Temporal shift: compare three points in time ───────────────────
	fmt.Println("\n=== 5. Temporal shift: who was present at each date? ===")
	for _, ts := range []string{"2024-01-01", "2024-06-01", "2025-01-01"} {
		r, err := client.Query(ctx,
			fmt.Sprintf("SELECT COUNT(*) AS n FROM Person AS OF '%s'", ts))
		if err != nil {
			fmt.Printf("  %s: error — %v\n", ts, err)
			continue
		}
		var n any = "?"
		if len(r.Rows) > 0 {
			n = r.Rows[0]["n"]
		}
		fmt.Printf("  %s: %v row(s)\n", ts, n)
	}

	// ── 6. Fluent builder: AsOf() + WithProvenance() ──────────────────────
	fmt.Println("\n=== 6. QueryBuilder: NewQuery(...).AsOf(...).WithProvenance().Execute() ===")
	result, err := relata.NewQuery("SELECT _pk, name FROM Person").
		Purpose("analytics").
		AsOf("2024-06-01").
		WithProvenance().
		Limit(5).
		Execute(ctx, client)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	fmt.Printf("  %d row(s):\n", len(result.Rows))
	for _, row := range result.Rows {
		fmt.Printf("    %v\n", row)
	}
}

func run(ctx context.Context, client *relata.Client, sql string) {
	result, err := client.Query(ctx, sql)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	fmt.Printf("  %d row(s):\n", len(result.Rows))
	for _, row := range result.Rows {
		fmt.Printf("    %v\n", row)
	}
}
