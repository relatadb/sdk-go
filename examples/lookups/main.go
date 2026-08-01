// Command lookups demonstrates CSV enrichment at query time via /lookup/*
// (#967 / #79).
//
// Lookup tables are small, read-only dictionaries the server joins into queries
// at plan time. Classic use case: enriching a country code or currency code
// into a human-readable label without denormalising it into every row.
//
// Flow:
//  1. RegisterLookup   — upload name + CSV (keyColumn + valueColumns)
//  2. ListLookups      — confirm it landed
//  3. InvokeLookup     — read back one value by key
//
// Usage:
//
//	go run ./examples/lookups -url http://localhost:9090 -token $RELATA_TOKEN
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

const countryCSV = `alpha2,alpha3,name
PK,PAK,Pakistan
IN,IND,India
AE,ARE,United Arab Emirates
SA,SAU,Saudi Arabia
`

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (optional)")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "analytics",
		Timeout:        30 * time.Second,
	})
	identity := relata.NewIdentityClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. Register the country_alpha3 lookup ─────────────────────────────
	fmt.Println("=== 1. RegisterLookup: country_alpha3 ===")
	registered, err := identity.RegisterLookup(ctx, "country_alpha3", countryCSV, "alpha2",
		&relata.RegisterLookupOptions{ValueColumns: []string{"alpha3", "name"}})
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  Registered: %v\n", registered)
	}

	// ── 2. List all registered lookup tables ──────────────────────────────
	fmt.Println("\n=== 2. ListLookups ===")
	lookups, err := identity.ListLookups(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	}
	fmt.Printf("  %d lookup(s) registered:\n", len(lookups))
	for i, l := range lookups {
		if i >= 10 {
			break
		}
		fmt.Printf("    - %v (size=%v)\n", l["name"], l["size"])
	}

	// ── 3. Invoke a lookup by key ─────────────────────────────────────────
	fmt.Println("\n=== 3. InvokeLookup: country_alpha3['PK'] ===")
	val, err := identity.InvokeLookup(ctx, "country_alpha3", "PK")
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  PK → %v\n", val)
	}

	// ── 4. Try a missing key ──────────────────────────────────────────────
	fmt.Println("\n=== 4. InvokeLookup: missing key 'ZZ' ===")
	if _, err := identity.InvokeLookup(ctx, "country_alpha3", "ZZ"); err != nil {
		fmt.Printf("  Got expected error: %v\n", err)
	} else {
		fmt.Println("  (no error)")
	}

	// ── 5. Use the lookup inside a SQL query (server-side JOIN) ───────────
	fmt.Println("\n=== 5. SQL JOIN via LOOKUP() function ===")
	result, err := client.Query(ctx,
		"SELECT _pk, passport_country, LOOKUP('country_alpha3', passport_country) AS iso3 FROM Person LIMIT 5",
	)
	if err != nil {
		var perr *relata.RelataError
		if errors.As(err, &perr) {
			fmt.Printf("  relata error: %s\n", perr)
		} else {
			fmt.Printf("  error: %v\n", err)
		}
		return
	}
	for _, row := range result.Rows {
		fmt.Printf("    %v\n", row)
	}
}
