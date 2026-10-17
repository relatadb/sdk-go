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
//
// # Read surface
//
// This Go client currently exposes only Upsert and BatchUpsert. The current
// server also supports governed GET /objects/{type}/{id} and GET /objects/{type}
// (see the TypeScript ObjectClient); their absence here is an SDK surface
// difference, not a server DELETE-only limitation. Use Client.Query for a
// governed point lookup or scan until a Go read convenience method is added.
// BatchUpsert below uses queued POST /ingest, unlike Python/TypeScript's
// POST /ingest/bulk helper: inspect task completion before relying on visibility.
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
// key; re-upserting with the same id supersedes bi-temporally.
//
// A schema rejection is NOT returned as an error: POST /ingest is an
// asynchronous queue-ack, so a rejected row yields a nil error and a payload
// like {"rows_queued":0,"rows_rejected":1,"errors":["Type.field: minCount 1
// not met"]}. Callers MUST check rows_rejected / errors on every call. Returns
// the ingest ack (rows_queued, rows_rejected, errors, task_id, ...), not a
// per-row receipt.
func (o *ObjectClient) Upsert(ctx context.Context, objectType, objectID string, fields map[string]any, opts *UpsertOptions) (map[string]any, error) {
	row := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		row[k] = v
	}
	row["id"] = objectID
	if opts != nil && opts.Source != "" {
		row["_source"] = opts.Source
	}
	body, err := rowToNDJSON([]map[string]any{row})
	if err != nil {
		return nil, err
	}
	return o.postIngest(ctx, objectType, body, "application/x-ndjson", opts)
}

// BatchUpsert bulk-upserts. Local encoding errors reject the entire request
// before HTTP. Each row must carry its own "id" key. Per-row
// rejections are reported in the receipt, not as an error — inspect it. Returns
// the bulk receipt (accepted, rejected, write_seq, queue_depth).
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
	body, err := rowToNDJSON(rows)
	if err != nil {
		return nil, err
	}
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
		return nil, errorFromStatus(status, data, "", 0, "", "")
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
// Encoding errors reject the entire local request before any HTTP mutation.
func rowToNDJSON(rows []map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for index, r := range rows {
		if err := enc.Encode(r); err != nil {
			// Return no prefix: callers must refuse before any HTTP effect.
			return "", fmt.Errorf("relata: encode NDJSON row %d: %w", index+1, err)
		}
	}
	out := buf.String()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}
