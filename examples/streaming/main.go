// Command streaming demonstrates SSE watch + transparency log (#967 Tier 2b).
//
// Two related real-time surfaces:
//  1. StreamingClient.Watch — /watch/stream pushes RowsAppended events as
//     commits matching the watch SQL land. Reconnects with exponential
//     backoff. This is what agent frameworks subscribe to for live updates.
//  2. LogClient — /log/append|head|loadLeaves is the tamper-evident audit
//     append-log. Every governed write lands here as a leaf.
//
// The example consumes watch events for 5 seconds, performs a write to trigger
// an event, then queries the transparency log.
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
	streaming := relata.NewStreamingClient(client)
	logClient := relata.NewLogClient(client)

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

	fmt.Println("\n=== 2. Issuing a write to trigger watch events ===")
	if _, err := client.Query(writeCtx,
		"INSERT INTO Person (_pk, name) VALUES ('watch-1', 'Watch Test 1')"); err != nil {
		fmt.Printf("  write error: %v\n", err)
	} else {
		fmt.Println("  inserted Person watch-1")
	}

	// ── 3. Alerts stream URL (for an external curl consumer) ──────────────
	fmt.Println("\n=== 3. Alerts stream URL (for external consumer) ===")
	fmt.Printf("  %s/alerts/stream\n", *url)

	// ── 4. Transparency log: append + head + leaves ───────────────────────
	logCtx, logCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer logCancel()

	fmt.Println("\n=== 4. Transparency log (append + head + loadLeaves) ===")
	index, _, err := logClient.Append(logCtx, []byte(`{"action":"example_run"}`))
	if err != nil {
		fmt.Printf("  append error: %v\n", err)
	} else {
		fmt.Printf("  appended leaf at index=%d\n", index)
	}
	head, err := logClient.Head(logCtx)
	if err != nil {
		fmt.Printf("  head error: %v\n", err)
	} else {
		fmt.Printf("  log head (tree size) = %d\n", head)
	}
	leaves, err := logClient.LoadLeaves(logCtx, nil)
	if err != nil {
		fmt.Printf("  loadLeaves error: %v\n", err)
	} else {
		fmt.Printf("  %d leaf/leaves loaded\n", len(leaves))
	}
}
