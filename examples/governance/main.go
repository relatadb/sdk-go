// Command governance demonstrates PURPOSE declaration, audit trail, type listing.
//
// Usage:
//
//	go run ./examples/governance -url http://localhost:9090 -token $RELATA_TOKEN
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

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken: *token,
		Timeout:     30 * time.Second,
	})
	audit := relata.NewAuditClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Query under 'analytics' purpose ────────────────────────────────
	fmt.Println("=== 1. Query under PURPOSE analytics ===")
	r1, err := client.Query(ctx, "SELECT * FROM Person LIMIT 3",
		relata.WithPurpose("analytics"))
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Rows returned: %d\n", len(r1.Rows))
	}

	// ── 2. Same query under 'audit' purpose ───────────────────────────────
	fmt.Println("\n=== 2. Query under PURPOSE audit ===")
	r2, err := client.Query(ctx, "SELECT * FROM Person LIMIT 3",
		relata.WithPurpose("audit"))
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Rows returned: %d\n", len(r2.Rows))
	}

	// ── 3. Audit log ──────────────────────────────────────────────────────
	fmt.Println("\n=== 3. Audit log ===")
	count, err := audit.Count(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Audit count: %v\n", count)
	}
	entries, err := audit.Entries(ctx, &relata.AuditEntriesOptions{Limit: 5})
	if err != nil {
		fmt.Printf("  entries error: %v\n", err)
	} else {
		fmt.Printf("  Recent entries: %v\n", entries)
	}

	// ── 4. List registered object types ───────────────────────────────────
	fmt.Println("\n=== 4. Registered object types ===")
	typesResp, err := client.ListTypes(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	fmt.Printf("  Types: %v\n", typesResp)
}
