// Command ingest demonstrates batch + CSV ingest via IngestClient.
//
// Usage:
//
//	go run ./examples/ingest -url http://localhost:9090 -token $RELATA_TOKEN
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
		DefaultPurpose: "data-load",
		Timeout:        30 * time.Second,
	})
	ingest := relata.NewIngestClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Bulk NDJSON ingest ─────────────────────────────────────────────
	fmt.Println("=== 1. Bulk NDJSON ingest (3 rows) ===")
	batch := []map[string]any{
		{"_pk": "p1", "name": "Alice", "email": "alice@example.com", "age": 30},
		{"_pk": "p2", "name": "Bob", "email": "bob@example.com", "age": 25},
		{"_pk": "p3", "name": "Carol", "email": "carol@example.com", "age": 35},
	}
	receipt, err := ingest.Bulk(ctx, "Person", batch, nil)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Receipt: %v\n", receipt)
	}

	// ── 2. CSV ingest ─────────────────────────────────────────────────────
	fmt.Println("\n=== 2. CSV ingest ===")
	csv := "_pk,name,email\np4,Dave,dave@example.com\np5,Eve,eve@example.com\n"
	csvReceipt, err := ingest.BulkCSV(ctx, "Person", csv, nil)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Receipt: %v\n", csvReceipt)
	}

	// ── 3. Verify queryable ───────────────────────────────────────────────
	fmt.Println("\n=== 3. Verify rows are queryable ===")
	result, err := client.Query(ctx, "SELECT name, age FROM Person ORDER BY name LIMIT 10")
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	fmt.Printf("  %d row(s) returned\n", len(result.Rows))
}
