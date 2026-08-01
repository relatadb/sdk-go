// Command parameterized demonstrates server-side $N parameter binding (#1162).
//
// $N placeholders are substituted safely by the server before planning — no
// string concatenation, no SQL injection. Use this for any query that takes
// caller-supplied values.
//
// Usage:
//
//	go run ./examples/parameterized -url http://localhost:9090 -token $RELATA_TOKEN
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
		BearerToken:    *token,
		DefaultPurpose: "analytics",
		Timeout:        30 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Filter by a single value ($1) ──────────────────────────────────
	fmt.Println("=== 1. Single-parameter filter (age = $1) ===")
	r, err := client.QueryWithParams(ctx,
		"SELECT name, age FROM Person WHERE age = $1",
		[]any{30},
	)
	printRows(r, err)

	// ── 2. Multiple typed parameters ──────────────────────────────────────
	fmt.Println("\n=== 2. Multi-type parameters (age > $1 AND city = $2) ===")
	r, err = client.QueryWithParams(ctx,
		"SELECT name, age, city FROM Person WHERE age > $1 AND city = $2 ORDER BY name",
		[]any{25, "Karachi"},
	)
	printRows(r, err)

	// ── 3. NULL + bool + float binding ────────────────────────────────────
	fmt.Println("\n=== 3. NULL / bool / float parameters ===")
	r, err = client.QueryWithParams(ctx,
		"SELECT _pk FROM Sample WHERE flagged = $1 AND score >= $2 AND note IS NOT DISTINCT FROM $3",
		[]any{true, 0.85, nil},
	)
	printRows(r, err)

	// ── 4. IN-list via repeated placeholders ──────────────────────────────
	fmt.Println("\n=== 4. IN-list via repeated placeholders ===")
	r, err = client.QueryWithParams(ctx,
		"SELECT _pk FROM Person WHERE _pk IN ($1, $2, $3)",
		[]any{"p1", "p2", "p3"},
	)
	printRows(r, err)
}

func printRows(r *relata.QueryResult, err error) {
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	if r == nil || len(r.Rows) == 0 {
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
