// Command basic demonstrates the minimal usage pattern for the Relata Go SDK.
// It connects to a local Relata server, checks health, and runs a simple
// SELECT query.
//
// Usage:
//
//	go run ./examples/basic \
//	    -url http://localhost:8080 \
//	    -token $RELATA_BEARER_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/OpenWorkBench-Co/RelataDB/sdks/go/relata"
)

func main() {
	url := flag.String("url", "http://localhost:8080", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (optional)")
	flag.Parse()

	// Construct the client. DefaultPurpose is set here so that plain
	// client.Query calls do not require an explicit WithPurpose option.
	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "investigation",
		Timeout:        30 * time.Second,
	})

	ctx := context.Background()

	// ── 1. Check node health ──────────────────────────────────────────────────
	health, err := client.Health(ctx)
	if err != nil {
		log.Fatalf("health check failed: %v", err)
	}
	fmt.Printf("Node  : %s  profile=%s  status=%s\n",
		health.NodeID, health.Profile, health.Status)

	// ── 2. Fetch operational status ───────────────────────────────────────────
	status, err := client.Status(ctx)
	if err != nil {
		log.Fatalf("status failed: %v", err)
	}
	fmt.Printf("Role  : %s  quota=%d\n", status.Role, status.QueryQuota)

	// ── 3. Run a basic SELECT ─────────────────────────────────────────────────
	// DefaultPurpose="investigation" is inherited from ClientOptions; no
	// WithPurpose option is required here.
	result, err := client.Query(ctx, "SELECT * FROM Person LIMIT 10")
	if err != nil {
		log.Fatalf("query failed: %v", err)
	}

	fmt.Printf("\nFound %d rows in %d ms  (query_id=%s)\n",
		result.RowCount, result.ElapsedMs, result.QueryID)

	// Iterate results using the Each helper.
	result.Each(func(row map[string]any) {
		fmt.Printf("  %v\n", row)
	})

	// ── 4. Extract a single column ───────────────────────────────────────────
	names := result.Column("name")
	fmt.Printf("\nNames: %v\n", names)
}
