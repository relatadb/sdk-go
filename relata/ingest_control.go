package relata

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// IngestControlClient wraps the ingestion control plane (#5689, #5714): source
// checkpoints, versioned data contracts, contract quality and quarantine. The
// routes use the data-plane credential and are scoped to the caller's tenant.
type IngestControlClient struct {
	c *Client
}

// NewIngestControlClient inherits the parent client's auth and tenant context.
func NewIngestControlClient(c *Client) *IngestControlClient {
	return &IngestControlClient{c: c}
}

// QuarantineListOptions filters ListQuarantine. Zero values use server defaults.
type QuarantineListOptions struct {
	// Dataset restricts the page to one dataset.
	Dataset string
	// AfterID continues from a previous page's next_after_id.
	AfterID uint64
	// Limit bounds the page size (server clamps to 1..1000).
	Limit int
}

func (i *IngestControlClient) getMap(ctx context.Context, path string) (map[string]any, error) {
	var out map[string]any
	if err := i.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Checkpoint calls GET /v1/ingest/checkpoints/{source}: the last durably committed checkpoint.
func (i *IngestControlClient) Checkpoint(ctx context.Context, source string) (map[string]any, error) {
	return i.getMap(ctx, "/v1/ingest/checkpoints/"+pathSegment(source))
}

// ListContracts calls GET /v1/ingest/contracts: {"contracts": [{"dataset", "latest_version"}]}.
func (i *IngestControlClient) ListContracts(ctx context.Context) (map[string]any, error) {
	return i.getMap(ctx, "/v1/ingest/contracts")
}

// GetContract calls GET /v1/ingest/contracts/{dataset}: every retained contract version.
func (i *IngestControlClient) GetContract(ctx context.Context, dataset string) (map[string]any, error) {
	return i.getMap(ctx, "/v1/ingest/contracts/"+pathSegment(dataset))
}

// PutContract calls PUT /v1/ingest/contracts/{dataset}: define the next contract
// version. The server checks backward compatibility; contract is sent verbatim.
func (i *IngestControlClient) PutContract(ctx context.Context, dataset string, contract map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := i.c.putJSON(ctx, "/v1/ingest/contracts/"+pathSegment(dataset), contract, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ContractQuality calls GET /v1/ingest/contracts/{dataset}/quality: validation
// counts, freshness and schema drift.
func (i *IngestControlClient) ContractQuality(ctx context.Context, dataset string) (map[string]any, error) {
	return i.getMap(ctx, "/v1/ingest/contracts/"+pathSegment(dataset)+"/quality")
}

// ListQuarantine calls GET /v1/ingest/quarantine. opts may be nil.
func (i *IngestControlClient) ListQuarantine(ctx context.Context, opts *QuarantineListOptions) (map[string]any, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Dataset != "" {
			q.Set("dataset", opts.Dataset)
		}
		if opts.AfterID > 0 {
			q.Set("after_id", strconv.FormatUint(opts.AfterID, 10))
		}
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
	}
	path := "/v1/ingest/quarantine"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	return i.getMap(ctx, path)
}

// DeleteQuarantine calls DELETE /v1/ingest/quarantine/{id}: discard one record
// (404 when unknown). It acknowledges the server's empty HTTP 204.
func (i *IngestControlClient) DeleteQuarantine(ctx context.Context, id uint64) error {
	var raw rawHTTPResponse
	path := "/v1/ingest/quarantine/" + strconv.FormatUint(id, 10)
	if err := i.c.doRequestWithHeaders(ctx, http.MethodDelete, path, nil, "", &raw, nil); err != nil {
		return err
	}
	if raw.StatusCode != http.StatusNoContent {
		return fmt.Errorf("relata: quarantine delete expected HTTP 204, got %d", raw.StatusCode)
	}
	return nil
}
