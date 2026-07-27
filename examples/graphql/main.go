// Command graphql demonstrates the governed `/graphql` door (ADR-220).
//
// Shows the four supported query shapes plus error handling:
//  1. Schema introspection (`{ __schema { types { name } } }`)
//  2. Simple field selection (`{ Person { _pk name email } }`)
//  3. Filtered + limited selection with `where` + `limit` args
//  4. Variables bound server-side
//  5. Error envelope from an unsupported operation (mutation)
//
// The server translates a GraphQL subset to SQL and runs it through the same
// governed query path as a normal POST /query. Mutations, subscriptions,
// fragments, and unions are not supported — the server returns a non-empty
// "errors" array and the SDK surfaces it as a Go error.
//
// Usage:
//
//	go run ./examples/graphql -url http://localhost:9090 -token $RELATA_TOKEN
package main

import (
	"context"
	"encoding/json"
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

	// ── 1. Introspection ──────────────────────────────────────────────────
	fmt.Println("=== 1. GraphQL introspection ===")
	schemaRaw, err := client.GraphQL(ctx, "{ __schema { types { name } } }", nil, "")
	must(ctx, err, "introspection")
	if sch, ok := schemaRaw.(map[string]any); ok {
		types, _ := sch["__schema"].(map[string]any)["types"].([]any)
		fmt.Printf("  %d registered type(s):\n", len(types))
		for i, t := range types {
			if i >= 10 {
				break
			}
			if m, ok := t.(map[string]any); ok {
				fmt.Printf("    - %s\n", m["name"])
			}
		}
	}

	// ── 2. Simple field selection ─────────────────────────────────────────
	fmt.Println("\n=== 2. Field selection: Person ===")
	personsRaw, err := client.GraphQL(ctx, "{ Person { _pk name email } }", nil, "")
	must(ctx, err, "person select")
	persons, _ := personsRaw.([]any)
	fmt.Printf("  %d row(s):\n", len(persons))
	for i, p := range persons {
		if i >= 5 {
			break
		}
		row, _ := p.(map[string]any)
		fmt.Printf("    _pk=%-12v  name=%-20v  email=%v\n",
			row["_pk"], row["name"], row["email"])
	}

	// ── 3. Filtered selection with `where` + `limit` ──────────────────────
	fmt.Println("\n=== 3. Filtered selection: limit + where ===")
	filtered, err := client.GraphQL(ctx,
		`{ Person(limit: 3, where: { _pk: "p1" }) { _pk name } }`, nil, "")
	must(ctx, err, "filtered")
	out, _ := json.Marshal(filtered)
	if len(out) > 200 {
		out = out[:200]
	}
	fmt.Printf("  Response: %s\n", string(out))

	// ── 4. Variables bound server-side ────────────────────────────────────
	fmt.Println("\n=== 4. Variables: $limit ===")
	vars := map[string]any{"limit": 2}
	withVars, err := client.GraphQL(ctx,
		"query Limited($limit: Int!) { Person(limit: $limit) { _pk name } }",
		vars, "Limited")
	must(ctx, err, "variables")
	out, _ = json.Marshal(withVars)
	if len(out) > 200 {
		out = out[:200]
	}
	fmt.Printf("  Response: %s\n", string(out))

	// ── 5. Mutation → expected error envelope ─────────────────────────────
	fmt.Println("\n=== 5. Mutation → expected error envelope ===")
	if _, err := client.GraphQL(ctx,
		`mutation { createPerson(name: "Bob") { id } }`, nil, ""); err != nil {
		fmt.Printf("  Got expected error: %v\n", err)
	} else {
		fmt.Println("  (unexpected success — server may have added mutation support)")
	}
}

func must(_ context.Context, err error, label string) {
	if err != nil {
		panic(fmt.Sprintf("%s failed: %v", label, err))
	}
}
