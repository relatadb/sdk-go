package relata

import (
	"context"
	"encoding/json"
	"fmt"
)

// IngestClient is the synchronous bulk-ingest client. It surfaces the server's
// POST /ingest?object_type=<Type> for NDJSON / CSV batches, plus media-upload
// status polling. Distinct from Client.IngestDocument which targets the
// datagrep-extractor envelope.
type IngestClient struct {
	c       *Client
	purpose string
}

// NewIngestClient constructs an IngestClient that inherits the parent client's
// auth, tenant, and default-purpose context. Mirrors the Python reference's
// IngestClient.from_client.
func NewIngestClient(c *Client) *IngestClient {
	return &IngestClient{c: c, purpose: c.defaultPurpose}
}

// BulkOptions configures per-call overrides for the bulk ingest methods.
type BulkOptions struct {
	// Purpose overrides the parent client's default purpose.
	Purpose string
}

// Bulk bulk-ingests rows as NDJSON. Returns the server receipt.
func (i *IngestClient) Bulk(ctx context.Context, objectType string, rows []map[string]any, opts *BulkOptions) (map[string]any, error) {
	body := rowToNDJSON(rows)
	return i.postIngest(ctx, objectType, body, "application/x-ndjson", opts)
}

// BulkCSV bulk-ingests a CSV string. The server parses it server-side.
func (i *IngestClient) BulkCSV(ctx context.Context, objectType, csvText string, opts *BulkOptions) (map[string]any, error) {
	return i.postIngest(ctx, objectType, csvText, "text/csv", opts)
}

// IngestIter streams rows from an iterator channel into batched POST /ingest
// calls with backpressure. Memory is O(batchSize). Returns the total number of
// rows successfully ingested. Stops on the first error.
//
//	rowsCh := make(chan map[string]any, 100)
//	go func() { defer close(rowsCh); for _, r := range bigDataset { rowsCh <- r } }()
//	total, err := ingest.IngestIter(ctx, "Person", rowsCh, "onboarding", 500)
func (i *IngestClient) IngestIter(ctx context.Context, objectType string, rows <-chan map[string]any, purpose string, batchSize int) (int, error) {
	if batchSize < 1 {
		batchSize = 500
	}
	opts := &BulkOptions{Purpose: purpose}
	total := 0
	batch := make([]map[string]any, 0, batchSize)
	for row := range rows {
		batch = append(batch, row)
		if len(batch) >= batchSize {
			if _, err := i.Bulk(ctx, objectType, batch, opts); err != nil {
				return total, err
			}
			total += len(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if _, err := i.Bulk(ctx, objectType, batch, opts); err != nil {
			return total, err
		}
		total += len(batch)
	}
	return total, nil
}

// MediaStatus polls the status of a multipart media upload.
func (i *IngestClient) MediaStatus(ctx context.Context, taskID string) (map[string]any, error) {
	var resp map[string]any
	if err := i.c.get(ctx, fmt.Sprintf("/ingest/media/%s", taskID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// postIngest sends a raw body to /ingest?object_type=… and decodes the receipt.
func (i *IngestClient) postIngest(ctx context.Context, objectType, body, contentType string, opts *BulkOptions) (map[string]any, error) {
	purpose := i.purpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	params := map[string]string{"object_type": objectType}
	if purpose != "" {
		params["purpose"] = purpose
	}
	status, data, _, err := i.c.rawHTTPRequest(ctx, "POST", encodeGetURL("/ingest", params), []byte(body), contentType)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, errorFromStatus(status, data, "", 0)
	}
	var resp map[string]any
	if len(data) == 0 {
		return resp, nil
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("relata: decode response: %w", err)
	}
	return resp, nil
}
