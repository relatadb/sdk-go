// Command analytics demonstrates a SQL-exploration pattern: filter, group-by,
// order-by, and window functions against the governed query path.
//
// Usage:
//
//	go run ./examples/analytics -url http://localhost:9090 -token $RELATA_TOKEN
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
		Timeout:        60 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Group-by + count ───────────────────────────────────────────────
	fmt.Println("=== 1. Group-by nationality (top 5) ===")
	r, err := client.Query(ctx,
		"SELECT nationality, COUNT(*) AS n FROM Person "+
			"GROUP BY nationality ORDER BY n DESC LIMIT 5")
	printRows(r, err)

	// ── 2. Histogram (CASE buckets) ───────────────────────────────────────
	fmt.Println("\n=== 2. Age histogram ===")
	r, err = client.Query(ctx,
		"SELECT CASE WHEN age < 18 THEN '<18' "+
			"WHEN age BETWEEN 18 AND 30 THEN '18-30' "+
			"WHEN age BETWEEN 31 AND 50 THEN '31-50' "+
			"ELSE '>50' END AS bucket, COUNT(*) AS n "+
			"FROM Person GROUP BY bucket ORDER BY bucket")
	printRows(r, err)

	// ── 3. Window function: top-2 oldest per country ──────────────────────
	fmt.Println("\n=== 3. Window function — ROW_NUMBER() per nationality ===")
	r, err = client.Query(ctx,
		"SELECT nationality, name, age FROM ("+
			"  SELECT nationality, name, age, "+
			"    ROW_NUMBER() OVER (PARTITION BY nationality ORDER BY age DESC) AS rn "+
			"  FROM Person"+
			") WHERE rn <= 2 ORDER BY nationality, rn")
	printRows(r, err)

	// ── 4. Bi-temporal comparison ─────────────────────────────────────────
	fmt.Println("\n=== 4. Bi-temporal delta — persons added since 2024-01-01 ===")
	r, err = client.Query(ctx,
		"SELECT COUNT(*) AS new_count FROM Person "+
			"WHERE system_from > '2024-01-01T00:00:00Z'")
	printRows(r, err)
}

func printRows(r *relata.QueryResult, err error) {
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	if len(r.Rows) == 0 {
		fmt.Println("  (empty result)")
		return
	}
	fmt.Printf("  %d row(s):\n", len(r.Rows))
	for i, row := range r.Rows {
		if i >= 10 {
			break
		}
		fmt.Printf("    %v\n", row)
	}
}
