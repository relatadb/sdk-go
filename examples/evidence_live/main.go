// Command evidence_live fetches and replays one retrieval evidence record
// through the Go SDK against a live server and prints the outcomes as JSON.
// It is driven by scripts/sdk_e2e_test.py (phase 18): the record is created by
// the harness, this program only exercises EvidenceClient.Get and Replay.
//
//	go run ./examples/evidence_live http://127.0.0.1:9090 <token> <evidence-id>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/relatadb/sdk-go/v3/relata"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: evidence_live <base-url> <token> <evidence-id>")
		os.Exit(2)
	}
	client := relata.New(os.Args[1], &relata.ClientOptions{
		BearerToken:          os.Args[2],
		AllowCleartextBearer: true,
	})
	evidence := relata.NewEvidenceClient(client)
	ctx := context.Background()
	envelope, err := evidence.Get(ctx, os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "get:", err)
		os.Exit(1)
	}
	report, err := evidence.Replay(ctx, os.Args[3], false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
	retrieval, _ := envelope["retrieval"].(map[string]any)
	out := map[string]any{
		"door":     retrieval["door"],
		"contract": envelope["contract"],
		"outcomes": report["summary"],
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
}
