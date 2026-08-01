// Command tokens demonstrates the dedup-token primitive (replay defence, #84).
//
// Flow:
//  1. Remember(tokenID) → true on first insert, false on duplicate
//  2. Check(tokenID)    → presence probe without mutation
//  3. Stats()           → dedup-set size, oldest entry, eviction policy
//  4. Revoke(tokenID)   → explicit removal (admin only)
//
// Usage:
//
//	go run ./examples/tokens -url http://localhost:9090 -token $RELATA_TOKEN
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
		DefaultPurpose: "admin",
		Timeout:        30 * time.Second,
	})
	tokens := relata.NewTokenClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tokenID := "replay-defence-7QT3"
	ttl := int64(3600)

	// ── 1. Atomic test-and-set ────────────────────────────────────────────
	fmt.Printf("=== 1. Remember('%s') ===\n", tokenID)
	first, err := tokens.Remember(ctx, tokenID, &relata.TokenRememberOptions{TTLSecs: ttl})
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	}
	fmt.Printf("  first call:  newly_inserted=%v  (expected true)\n", first)
	second, err := tokens.Remember(ctx, tokenID, &relata.TokenRememberOptions{TTLSecs: ttl})
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	}
	fmt.Printf("  second call: newly_inserted=%v  (expected false — duplicate)\n", second)

	// ── 2. Non-mutating presence check ────────────────────────────────────
	fmt.Printf("\n=== 2. Check('%s') ===\n", tokenID)
	info, err := tokens.Check(ctx, tokenID)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  present=%v  inserted_at_ns=%v  ttl=%v\n",
			info["present"], info["inserted_at_ns"], info["ttl_secs"])
	}

	// ── 3. Dedup-set stats ────────────────────────────────────────────────
	fmt.Println("\n=== 3. Stats() ===")
	stats, err := tokens.Stats(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  count=%d  oldest_ns=%d  eviction_policy=%s\n",
			stats.Count, stats.OldestNS, stats.EvictionPolicy)
	}

	// ── 4. Explicit revoke (admin only) ───────────────────────────────────
	fmt.Printf("\n=== 4. Revoke('%s') ===\n", tokenID)
	revoked, err := tokens.Revoke(ctx, tokenID)
	if err != nil {
		fmt.Printf("  (revoke requires admin — %v)\n", err)
	} else {
		fmt.Printf("  revoked=%v\n", revoked)
	}
}
