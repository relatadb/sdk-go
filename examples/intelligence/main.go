// Command intelligence demonstrates generic governed-query source retrieval
// alongside retained typed intelligence helpers. Ownership and transaction
// graph queries return source rows only; application analysis is caller-owned.
//   - ConvoyDetect               — find co-travelling entities
//   - DnsTunnelDetect            — local triage over governed DNS query pages
//
// The sanctions section fetches governed source rows only; matching and review
// remain application responsibilities. DNS tunnel triage composes the
// authenticated schema and query APIs; tenant, purpose, and row ACLs apply to
// every page fetched.
//
// Usage:
//
//	go run ./examples/intelligence \
//	    -url http://localhost:9090 -token $RELATA_TOKEN \
//	    -target "Acme Holdings Ltd"
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (optional)")
	target := flag.String("target", "Acme Holdings Ltd", "Party / entity to investigate")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "investigation",
		Timeout:        60 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// ── 1. Governed sanctions source rows ──────────────────────────────────
	fmt.Printf("=== 1. Sanctions source rows for: %s ===\n", *target)
	people, err := client.QueryWithParams(ctx, "SELECT id, name, dob FROM Person WHERE name = $1 LIMIT 100", []any{*target}, relata.WithPurpose("investigation"))
	if err != nil {
		log.Fatal(err)
	}
	entries, err := client.Query(ctx, "SELECT name, aliases, dob, list_name, program FROM SanctionEntry LIMIT 10000", relata.WithPurpose("investigation"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  fetched %d subject row(s) and %d sanctions-list row(s)\n", len(people.Rows), len(entries.Rows))
	if people.Truncated || people.NextCursor != nil || entries.Truncated || entries.NextCursor != nil {
		fmt.Println("  WARNING: source results are incomplete; do not treat this as a completed screen")
	}
	fmt.Println("  Apply your organization's documented matching and review policy to these rows.")

	// ── 2. Ownership source rows (retrieval only) ───────────────────────
	fmt.Println("\n=== 2. OwnershipEdge source rows (application analysis required) ===")
	ownership, err := client.Query(ctx, "SELECT subject, predicate, object, ownership_pct, jurisdiction FROM OwnershipEdge LIMIT 1000", relata.WithPurpose("investigation"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  fetched %d ownership source row(s)\n", len(ownership.Rows))
	if ownership.Truncated || ownership.NextCursor != nil {
		fmt.Println("  source result is incomplete; do not treat it as a complete ownership chain")
	}
	fmt.Println("  apply documented ownership traversal and legal review outside this SDK")

	// ── 3. Transaction source rows (retrieval only) ──────────────────────
	fmt.Printf("\n=== 3. TransactionGraph source rows for %s (retrieval only) ===\n", *target)
	transactions, err := client.Query(ctx, "SELECT from_wallet, to_wallet, amount, asset, chain, is_mixer FROM TransactionGraph LIMIT 1000", relata.WithPurpose("investigation"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  fetched %d transaction source row(s)\n", len(transactions.Rows))
	if transactions.Truncated || transactions.NextCursor != nil {
		fmt.Println("  source result is incomplete; do not treat it as a complete transaction trace")
	}
	fmt.Println("  apply approved graph-walk and attribution policy outside this SDK")

	// ── 4. Convoy detection ───────────────────────────────────────────────
	fmt.Println("\n=== 4. CONVOY_DETECT: radius=200m, time_tol=300s, min_points=2 ===")
	convoys, err := client.ConvoyDetect(ctx, "investigation", &relata.ConvoyDetectOptions{RadiusM: 200.0, TimeTolSecs: 300, MinPoints: 2})
	if err != nil {
		log.Fatal(err)
	}
	cRows, _ := convoys["rows"].([]any)
	fmt.Printf("  %d convoy(s) detected\n", len(cRows))
	for i, c := range cRows {
		if i >= 5 {
			break
		}
		row, _ := c.(map[string]any)
		fmt.Printf("    convoy_id=%v  members=%v  first_seen=%v\n",
			row["convoy_id"], row["members"], row["first_seen"])
	}

	// ── 5. DNS tunnel detection ───────────────────────────────────────────
	fmt.Println("\n=== 5. DNS tunnel triage ===")
	dns, err := client.DnsTunnelDetect(ctx, "investigation", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  %d anomalous DNS pattern(s); complete=%v rows_scanned=%d\n", len(dns.Rows), dns.Complete, dns.RowsScanned)
	for i, row := range dns.Rows {
		if i >= 5 {
			break
		}
		fmt.Printf("    domain=%-30s entropy=%.2f queries=%d max_label_len=%d confidence=%.2f\n",
			row.Domain, row.AvgEntropy, row.QueryCount, row.MaxLabelLen, row.Confidence)
	}
}
