// Command cdr_ingest ingests telecom CDR rows through the SDK adapter (#5744).
//
// The server has no telecom parser: IngestCDR maps aliases to canonical
// CdrRecord rows and posts them to generic POST /v1/ingest. Register the type
// once with the telco-cdr schema package.
//
//	go run ./examples/cdr_ingest -url http://localhost:9090 -token $RELATA_TOKEN
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
		DefaultPurpose: "investigation",
		Timeout:        30 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	receipt, err := relata.NewIngestClient(client).IngestCDR(ctx, []map[string]any{
		{"a_number": "+971 50 123 4567", "b_number": "971507654321", "timestamp": "2026-01-01T00:00:00Z", "duration": 60, "cell_id": "CELL-042", "type": "voice"},
		{"caller": "971501111111", "callee": "971502222222", "start": 1735689700, "site": "CELL-007", "record_type": "sms"},
		{"caller": "not-a-number"},
	}, nil)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	fmt.Printf("row errors: %v\n", receipt["errors"])
	taskID, _ := receipt["task_id"].(string)
	if taskID == "" {
		return
	}
	// Acceptance is not durability: poll the generic task for the outcome.
	for i := 0; i < 20; i++ {
		status, err := client.IngestDocumentStatus(ctx, taskID)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}
		if status.Status != "pending" || i == 19 {
			fmt.Printf("%s: %d written, %d dropped\n", status.Status, status.RowsWritten, status.RowsDropped)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}
