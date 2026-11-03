// Command streaming demonstrates the SSE watch + alerts streams (#967 Tier 2b).
//
// Related real-time surfaces:
//  1. StreamingClient.Watch — /v1/v1/watch/stream polls for governed query-result
//     notifications. The server emits hashes and explicit gap/failure events,
//     not changed row contents. Reconnects use the same in-memory
//     per-principal queue, so clients must re-register after server restart.
//  2. /v1/alerts/stream — the SSE alerts feed an external consumer can tail.
//
// The example consumes watch events for 5 seconds and performs a write to
// trigger an event.
//
// Usage:
//
//	go run ./examples/streaming -url http://localhost:9090 -token $RELATA_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/relatadb/sdk-go/v3/relata"
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
	streaming := relata.NewStreamingClient(client)
	ingest := relata.NewIngestClient(client)

	// ── 1. Watch with a 5-second deadline ─────────────────────────────────
	watchCtx, watchCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer watchCancel()

	fmt.Println("=== 1. StreamingClient.Watch('SELECT * FROM Person') — 5s window ===")
	iter, err := streaming.Watch(watchCtx, "SELECT * FROM Person", &relata.WatchOptions{Purpose: "analytics"})
	if err != nil {
		fmt.Printf("  watch open error: %v\n", err)
	} else {
		seen := 0
		for iter.Next() {
			seen++
			evt := iter.Event()
			fmt.Printf("    [%d] event=%s data=%v\n", seen, evt.Type, evt.Data)
		}
		if err := iter.Err(); err != nil && watchCtx.Err() == nil {
			fmt.Printf("  iterator ended with error: %v\n", err)
		}
		fmt.Printf("  watch window elapsed — saw %d event(s)\n", seen)
		iter.Close()
	}

	// ── 2. Issue a write to trigger watch events (next time you run it) ───
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer writeCancel()

	// /v1/query is read-only (#782) — writes go through the governed ingest
	// door, not SQL INSERT.
	fmt.Println("\n=== 2. Issuing a write to trigger watch events ===")
	rows := []map[string]any{{"_pk": "watch-1", "name": "Watch Test 1"}}
	if _, err := ingest.Bulk(writeCtx, "Person", rows, nil); err != nil {
		fmt.Printf("  write error: %v\n", err)
	} else {
		fmt.Println("  inserted Person watch-1")
	}

	// ── 3. Alerts stream URL (for an external curl consumer) ──────────────
	fmt.Println("\n=== 3. Alerts stream URL (for external consumer) ===")
	fmt.Printf("  %s/v1/alerts/stream\n", *url)
}
