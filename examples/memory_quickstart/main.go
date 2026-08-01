// Command memory_quickstart demonstrates the high-level Memory client (ADR-144).
//
// Mem0-style governed agent memory: add / search / recognise / consolidate /
// forget. Governance stays on: a non-empty purpose is required on every call;
// forget is a retention retract, not a hard delete.
//
// Usage:
//
//	go run ./examples/memory_quickstart -url http://localhost:9090 -token $RELATA_TOKEN
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

	mem, err := relata.NewMemory(*url, "agent-notes", &relata.MemoryOptions{
		BearerToken: *token,
		Timeout:     30 * time.Second,
	})
	if err != nil {
		fmt.Printf("NewMemory: %v\n", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 1. add — persist governed memories ────────────────────────────────
	fmt.Println("=== 1. add — persist governed memories ===")
	id1, err := mem.Add(ctx, "Alice prefers dark mode in the UI")
	if err != nil {
		fmt.Printf("  add: %v\n", err)
		return
	}
	id2, _ := mem.Add(ctx, "Bob's timezone is UTC+5:30")
	id3, _ := mem.Add(ctx, "The analytics dashboard is reviewed every Monday")
	fmt.Printf("  stored: %s, %s, %s\n", id1, id2, id3)

	// ── 2. search — hybrid recall ─────────────────────────────────────────
	fmt.Println("\n=== 2. search — hybrid recall ('UI preferences') ===")
	hits, err := mem.Search(ctx, "UI preferences", relata.WithTopK(3))
	if err != nil {
		fmt.Printf("  search: %v\n", err)
	} else {
		for i, h := range hits {
			fmt.Printf("  %d. [%.3f] %v\n", i+1, h["score"], h["text"])
		}
	}

	// ── 3. get — fetch a specific memory by id ────────────────────────────
	fmt.Printf("\n=== 3. get('%s') ===\n", id1)
	one, err := mem.Get(ctx, id1)
	if err != nil {
		fmt.Printf("  get: %v\n", err)
	} else {
		fmt.Printf("  %v\n", one)
	}

	// ── 4. update — consolidate an existing memory ────────────────────────
	fmt.Println("\n=== 4. update — consolidate ===")
	newID, err := mem.Update(ctx, id1, "Alice prefers dark mode; she also uses large fonts")
	if err != nil {
		fmt.Printf("  update: %v\n", err)
	} else {
		fmt.Printf("  updated → %s\n", newID)
	}

	// ── 5. forget — retention retract ─────────────────────────────────────
	fmt.Printf("\n=== 5. forget('%s') ===\n", id2)
	if _, err := mem.Forget(ctx, id2); err != nil {
		fmt.Printf("  forget: %v\n", err)
	} else {
		fmt.Println("  forgot")
	}

	// ── 6. justify — provenance chain for a memory ────────────────────────
	fmt.Printf("\n=== 6. justify('%s') ===\n", id3)
	if j, err := mem.Justify(ctx, id3); err != nil {
		fmt.Printf("  justify: %v\n", err)
	} else {
		fmt.Printf("  %v\n", j)
	}
}
