package relata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// ObjectClient is the synchronous typed upsert/load client for state objects.
// It wraps the universal upsert path POST /ingest?object_type=<Type> so callers
// do not have to know the ingest envelope shape.
type ObjectClient struct {
	c       *Client
	purpose string
}

// NewObjectClient constructs an ObjectClient that inherits the parent client's
// auth, tenant, and default-purpose context. Mirrors the Python reference's
// ObjectClient.from_client.
func NewObjectClient(c *Client) *ObjectClient {
	return &ObjectClient{c: c, purpose: c.defaultPurpose}
}

// UpsertOptions configures per-call overrides for ObjectClient.Upsert /
// BatchUpsert.
type UpsertOptions struct {
	// Purpose overrides the parent client's default purpose.
	Purpose string
	// Source attaches a provenance _source to every row.
	Source string
}

// Upsert upserts a single state object. The objectID becomes the row's primary
// key; re-upserting with the same id supersedes bi-temporally. Returns the
// server's upsert receipt (object_id, write_seq, valid_from).
func (o *ObjectClient) Upsert(ctx context.Context, objectType, objectID string, fields map[string]any, opts *UpsertOptions) (map[string]any, error) {
	row := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		row[k] = v
	}
	row["id"] = objectID
	if opts != nil && opts.Source != "" {
		row["_source"] = opts.Source
	}
	body := rowToNDJSON([]map[string]any{row})
	return o.postIngest(ctx, objectType, body, "application/x-ndjson", opts)
}

// BatchUpsert bulk-upserts. Each row must carry its own "id" key. Returns the
// bulk receipt (accepted, rejected, write_seq, queue_depth).
func (o *ObjectClient) BatchUpsert(ctx context.Context, objectType string, rows []map[string]any, opts *UpsertOptions) (map[string]any, error) {
	if opts != nil && opts.Source != "" {
		annotated := make([]map[string]any, len(rows))
		for i, r := range rows {
			cp := make(map[string]any, len(r)+1)
			for k, v := range r {
				cp[k] = v
			}
			cp["_source"] = opts.Source
			annotated[i] = cp
		}
		rows = annotated
	}
	body := rowToNDJSON(rows)
	return o.postIngest(ctx, objectType, body, "application/x-ndjson", opts)
}

// postIngest sends a raw body to /ingest?object_type=… with the resolved
// purpose, and decodes the JSON receipt.
func (o *ObjectClient) postIngest(ctx context.Context, objectType, body, contentType string, opts *UpsertOptions) (map[string]any, error) {
	purpose := o.purpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	params := map[string]string{"object_type": objectType}
	if purpose != "" {
		params["purpose"] = purpose
	}
	status, data, _, err := o.c.rawHTTPRequest(ctx, "POST", encodeGetURL("/ingest", params), []byte(body), contentType)
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

// rowToNDJSON serialises rows as newline-delimited JSON — one row per line.
// json.Encoder.Encode already appends a newline after each row, so the joined
// output matches Python's "\n".join(json.dumps(r) for r in rows).
func rowToNDJSON(rows []map[string]any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		_ = enc.Encode(r)
	}
	out := buf.String()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out
}
