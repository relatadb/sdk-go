// Command advanced_query demonstrates filtered SELECT + aggregate + Arrow result.
//
// Usage:
//
//	go run ./examples/advanced_query -url http://localhost:9090 -token $RELATA_TOKEN
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

	// ── 1. Filtered SELECT ────────────────────────────────────────────────
	fmt.Println("=== 1. Filtered SELECT ===")
	r, err := client.Query(ctx,
		"SELECT name, age FROM Person WHERE age > 25 ORDER BY name LIMIT 10")
	printRows(r, err)

	// ── 2. Aggregate ──────────────────────────────────────────────────────
	fmt.Println("\n=== 2. COUNT aggregate ===")
	r, err = client.Query(ctx, "SELECT COUNT(*) AS total FROM Person")
	printRows(r, err)

	// ── 3. Arrow bytes via StreamingClient.QueryArrowRaw ──────────────────
	fmt.Println("\n=== 3. Arrow bytes (StreamingClient.QueryArrowRaw) ===")
	streaming := relata.NewStreamingClient(client)
	iter, err := streaming.QueryArrowRaw(ctx,
		"SELECT name, age FROM Person LIMIT 5", nil)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	defer iter.Close()
	buf := make([]byte, 4096)
	total := 0
	for {
		n, readErr := iter.Read(buf)
		total += n
		if readErr != nil {
			break
		}
	}
	fmt.Printf("  Arrow stream consumed: %d bytes\n", total)
}

func printRows(r *relata.QueryResult, err error) {
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	fmt.Printf("  Rows: %d\n", len(r.Rows))
	for _, row := range r.Rows {
		fmt.Printf("    %v\n", row)
	}
}
