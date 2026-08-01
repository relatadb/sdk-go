// Command multi_tenant demonstrates organisation isolation: tenant A writes,
// tenant B cannot see the rows.
//
// The server must be running with RELATA_TENANCY_MODE=multi and two
// admin-issued bearer tokens for two different tenants.
//
// Usage:
//
//	RELATA_TOKEN_A=<token-a> RELATA_TOKEN_B=<token-b> \
//	  go run ./examples/multi_tenant -url http://localhost:9090
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	flag.Parse()

	tokenA := getenv("RELATA_TOKEN_A", "token-a")
	tokenB := getenv("RELATA_TOKEN_B", "token-b")

	fmt.Printf("Tenant A token : %s\n", tokenA)
	fmt.Printf("Tenant B token : %s\n", tokenB)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Tenant A: ingest a secret record ───────────────────────────────
	fmt.Println("\n=== Tenant A: ingest ===")
	clientA := relata.New(*url, &relata.ClientOptions{
		BearerToken:    tokenA,
		DefaultPurpose: "data-load",
		Timeout:        30 * time.Second,
	})
	ingestA := relata.NewIngestClient(clientA)
	receipt, err := ingestA.Bulk(ctx, "SecretRecord",
		[]map[string]any{{"_pk": "s1", "secret": "tenant-a-only", "value": 42}}, nil)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Tenant A receipt: %v\n", receipt)
	}

	// ── 2. Tenant A: query (should see ≥1 row) ────────────────────────────
	fmt.Println("\n=== Tenant A: query (should see ≥1 row) ===")
	r1, err := clientA.Query(ctx, "SELECT secret FROM SecretRecord LIMIT 10")
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Tenant A sees %d row(s)\n", len(r1.Rows))
	}

	// ── 3. Tenant B: query (should see 0 rows — org isolation) ────────────
	fmt.Println("\n=== Tenant B: query (should see 0 rows — org isolation) ===")
	clientB := relata.New(*url, &relata.ClientOptions{
		BearerToken:    tokenB,
		DefaultPurpose: "analytics",
		Timeout:        30 * time.Second,
	})
	r2, err := clientB.Query(ctx, "SELECT secret FROM SecretRecord LIMIT 10")
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Tenant B sees %d row(s)\n", len(r2.Rows))
	}
}

func getenv(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}
