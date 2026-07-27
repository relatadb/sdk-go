// Command intelligence demonstrates a chained KYC / sanctions sweep across
// the typed intelligence operators on the Go client (#967).
//
// Operators covered:
//   - SanctionsScreen            — fuzzy match against sanction lists
//   - BeneficialOwnershipChain   — trace to ultimate beneficial owner
//   - CryptoTrace                — follow cryptocurrency fund flow
//   - ConvoyDetect               — find co-travelling entities
//   - DnsTunnelDetect            — DNS exfiltration patterns
//
// Each operator maps to a governed SQL TVF (ADRs 109–115). PURPOSE is audited
// and ACLs apply on every call.
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
	"time"

	"github.com/relatadb/sdk-go/relata"
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

	// ── 1. Sanctions screening ────────────────────────────────────────────
	fmt.Printf("=== 1. SANCTIONS_SCREEN: %s (threshold=0.85) ===\n", *target)
	hits, err := client.SanctionsScreen(ctx, "investigation", *target, 0.85)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		hits = map[string]any{}
	}
	hitRows, _ := hits["rows"].([]any)
	fmt.Printf("  %d potential hit(s)\n", len(hitRows))
	for i, h := range hitRows {
		if i >= 5 {
			break
		}
		row, _ := h.(map[string]any)
		fmt.Printf("    list=%-20v score=%v  match=%v\n",
			row["list"], row["score"], row["matched_name"])
	}

	// ── 2. Beneficial ownership chain ─────────────────────────────────────
	fmt.Printf("\n=== 2. BENEFICIAL_OWNERSHIP_CHAIN: %s (max_depth=5) ===\n", *target)
	chain, err := client.BeneficialOwnershipChain(ctx, "investigation", *target, 5)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		chain = map[string]any{}
	}
	layers, _ := chain["rows"].([]any)
	if layers == nil {
		layers, _ = chain["layers"].([]any)
	}
	fmt.Printf("  %d layer(s) to UBO:\n", len(layers))
	for i, l := range layers {
		row, _ := l.(map[string]any)
		share := 0.0
		if s, ok := row["share"].(float64); ok {
			share = s * 100
		}
		fmt.Printf("    %d. %-30v owns %.2f%%  via %v\n", i+1, row["entity"], share, row["instrument"])
	}

	// ── 3. Crypto trace ───────────────────────────────────────────────────
	fmt.Printf("\n=== 3. CRYPTO_TRACE: %s ===\n", *target)
	trace, err := client.CryptoTrace(ctx, "investigation", *target)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		trace = map[string]any{}
	}
	hops, _ := trace["rows"].([]any)
	fmt.Printf("  %d hop(s) in fund-flow graph\n", len(hops))
	for i, h := range hops {
		if i >= 10 {
			break
		}
		row, _ := h.(map[string]any)
		fmt.Printf("    %2d. %v → %v  amount=%v\n", i+1, row["from"], row["to"], row["amount"])
	}

	// ── 4. Convoy detection ───────────────────────────────────────────────
	fmt.Println("\n=== 4. CONVOY_DETECT: radius=200m, time_tol=300s, min_points=2 ===")
	convoys, err := client.ConvoyDetect(ctx, "investigation", 200.0, 300, 2)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		convoys = map[string]any{}
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
	fmt.Printf("\n=== 5. DNS_TUNNEL_DETECT: %s ===\n", *target)
	dns, err := client.DnsTunnelDetect(ctx, "investigation", *target)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		dns = map[string]any{}
	}
	dRows, _ := dns["rows"].([]any)
	fmt.Printf("  %d anomalous DNS pattern(s)\n", len(dRows))
	for i, d := range dRows {
		if i >= 5 {
			break
		}
		row, _ := d.(map[string]any)
		fmt.Printf("    domain=%-30v entropy=%v  bytes=%v\n",
			row["domain"], row["entropy"], row["bytes_exchanged"])
	}
}
