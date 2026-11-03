// Command document_ingest submits prepared document rows (#5743).
//
// Extraction and chunking happen in an external processor or adapter; the
// server accepts only governed DocumentSource/DocumentChunk rows.
//
//	go run ./examples/document_ingest -url http://localhost:9090 -token $RELATA_TOKEN
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
		DefaultPurpose: "rag",
		Timeout:        30 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	receipt, err := client.IngestDocument(ctx, []map[string]any{
		{"_type": "DocumentSource", "_pk": "rpt-1", "report_id": "rpt-1", "source_filename": "paper.pdf", "parser": "my-adapter", "status": "complete", "total_chunks": 2},
		{"_type": "DocumentChunk", "_pk": "rpt-1:c0", "chunk_id": "rpt-1:c0", "report_id": "rpt-1", "sequence_index": 0, "prev_chunk_id": nil, "next_chunk_id": "rpt-1:c1", "page_start": 1, "page_end": 1, "section_path": "Abstract", "text_body": "First chunk."},
		{"_type": "DocumentChunk", "_pk": "rpt-1:c1", "chunk_id": "rpt-1:c1", "report_id": "rpt-1", "sequence_index": 1, "prev_chunk_id": "rpt-1:c0", "next_chunk_id": nil, "page_start": 2, "page_end": 2, "section_path": "Methods", "text_body": "Second chunk."},
	}, "")
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	fmt.Printf("queued %d, rejected %d %v\n", receipt.RowsQueued, receipt.RowsRejected, receipt.Errors)
	for i := 0; i < 40; i++ {
		status, err := client.IngestDocumentStatus(ctx, receipt.TaskID)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}
		if status.Status != "pending" || i == 39 {
			// Only "complete" means every requested row was written.
			fmt.Printf("%s: %d/%d %v\n", status.Status, status.RowsWritten, status.RowsTotal, status.DropReasons)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}
