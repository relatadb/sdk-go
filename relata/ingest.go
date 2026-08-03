package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	// DetectPacks is a per-call SmartIngest detector-pack override (#2258 SI-3),
	// sent as the X-Detect-Packs header. Use "none" to disable SmartIngest for
	// this call, or a comma-separated list (e.g. "network,financial") to select
	// packs. Requires the IngestDetectOverride ACL grant server-side.
	DetectPacks string
	// OnConflict sets the conflict-resolution strategy when set, routing the
	// call to POST /ingest/bulk (mixed-type JSON) instead of /ingest. One of
	// "upsert" | "skip" | "error". Empty = default server behaviour.
	OnConflict string
	// TenantID optionally scopes the write to a tenant (forwarded to the
	// governance layer). Mirrors BulkIngestRequest.tenant_id.
	TenantID string
}

// Bulk bulk-ingests rows as NDJSON. Returns the server receipt.
func (i *IngestClient) Bulk(ctx context.Context, objectType string, rows []map[string]any, opts *BulkOptions) (map[string]any, error) {
	if opts != nil && opts.OnConflict != "" {
		// Route to /ingest/bulk so on_conflict + tenant_id take effect
		// (the /ingest door doesn't accept them).
		return i.postBulkTyped(ctx, objectType, rows, opts)
	}
	body := rowToNDJSON(rows)
	return i.postIngest(ctx, objectType, body, "application/x-ndjson", opts)
}

// BulkCSV bulk-ingests a CSV string. The server parses it server-side.
func (i *IngestClient) BulkCSV(ctx context.Context, objectType, csvText string, opts *BulkOptions) (map[string]any, error) {
	return i.postIngest(ctx, objectType, csvText, "text/csv", opts)
}

// AutoOptions configures schemaless ingest (POST /ingest/auto, T6 #1988).
type AutoOptions struct {
	// Purpose overrides the parent client's default purpose.
	Purpose string
	// Schema overrides field-type inference, keyed by field name.
	Schema map[string]string
	// TenantID scopes the write to a tenant.
	TenantID string
}

// IngestAuto is a schemaless ingest — the type is auto-created on first write
// with inferred fields (T6, #1988). Wraps POST /ingest/auto. The response
// carries type_created + inferred_schema.
func (i *IngestClient) IngestAuto(ctx context.Context, objectType string, rows []map[string]any, opts *AutoOptions) (map[string]any, error) {
	purpose := i.purpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	body := map[string]any{"object_type": objectType, "rows": rows}
	if purpose != "" {
		body["purpose"] = purpose
	}
	if opts != nil {
		if len(opts.Schema) > 0 {
			body["schema"] = opts.Schema
		}
		if opts.TenantID != "" {
			body["tenant_id"] = opts.TenantID
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("relata: encode ingest/auto body: %w", err)
	}
	status, data, _, err := i.c.rawHTTPRequest(ctx, "POST", "/ingest/auto", encoded, "application/json")
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
	if err := i.c.get(ctx, "/ingest/media/"+pathSegment(taskID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// IngestCDR ingests CDR (call detail records) via POST /ingest/cdr (#2252).
//
// Each row is a map whose keys map to CDR fields (caller, callee, start,
// duration, tower, type); common aliases like caller_msisdn/a_number are
// accepted. Rows are serialised to the CSV shape the server's CdrBatch::from_csv
// expects. Purpose is mandatory server-side — pass it via opts or the parent
// client's default purpose.
func (i *IngestClient) IngestCDR(ctx context.Context, rows []map[string]any, opts *BulkOptions) (map[string]any, error) {
	body := rowsToCDRCSV(rows)
	status, data, _, err := i.c.rawHTTPRequest(ctx, "POST", encodeGetURL("/ingest/cdr", i.purposeParams(opts)), []byte(body), "text/csv")
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

// OTLPTraces ingests an OTLP/JSON trace payload via POST /v1/traces (#2252).
func (i *IngestClient) OTLPTraces(ctx context.Context, payload map[string]any, opts *BulkOptions) (map[string]any, error) {
	return i.otlp(ctx, "/v1/traces", payload, opts)
}

// OTLPLogs ingests an OTLP/JSON log payload via POST /v1/logs (#2252).
func (i *IngestClient) OTLPLogs(ctx context.Context, payload map[string]any, opts *BulkOptions) (map[string]any, error) {
	return i.otlp(ctx, "/v1/logs", payload, opts)
}

// OTLPMetrics ingests an OTLP/JSON metric payload via POST /v1/metrics (#2252).
func (i *IngestClient) OTLPMetrics(ctx context.Context, payload map[string]any, opts *BulkOptions) (map[string]any, error) {
	return i.otlp(ctx, "/v1/metrics", payload, opts)
}

// effectivePurpose resolves a per-call purpose override against the parent default.
func (i *IngestClient) effectivePurpose(opts *BulkOptions) string {
	if opts != nil && opts.Purpose != "" {
		return opts.Purpose
	}
	return i.purpose
}

// purposeParams builds the query-param map carrying only the effective purpose.
func (i *IngestClient) purposeParams(opts *BulkOptions) map[string]string {
	purpose := i.effectivePurpose(opts)
	if purpose == "" {
		return nil
	}
	return map[string]string{"purpose": purpose}
}

// otlp POSTs an OTLP/JSON payload to one of the /v1/{traces,logs,metrics} doors.
func (i *IngestClient) otlp(ctx context.Context, path string, payload map[string]any, opts *BulkOptions) (map[string]any, error) {
	var resp map[string]any
	if err := i.c.postJSON(ctx, encodeGetURL(path, i.purposeParams(opts)), payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ── CDR CSV serialisation (#2252) ─────────────────────────────────────────────

// cdrAliases maps the canonical CDR column to its accepted aliases — mirrors
// the server's CdrRecord::from_csv_row header recognition (case-insensitive).
var cdrAliases = map[string][]string{
	"caller":   {"caller", "caller_msisdn", "a_number", "a_msisdn"},
	"callee":   {"callee", "callee_msisdn", "b_number", "b_msisdn", "called"},
	"start":    {"start", "call_start", "start_time", "timestamp", "call_start_ns"},
	"duration": {"duration", "duration_secs", "duration_seconds"},
	"tower":    {"tower", "tower_id", "cell_id", "site", "cell"},
	"type":     {"type", "call_type", "record_type"},
}

var cdrColumns = []string{"caller", "callee", "start", "duration", "tower", "type"}

// cdrField renders a CDR cell value — the server parses CSV via naive split(',').
func cdrField(value any) string {
	if value == nil {
		return ""
	}
	// Strip commas / CR / LF so a value can never break column alignment.
	s := strings.ReplaceAll(fmt.Sprintf("%v", value), ",", "")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// rowsToCDRCSV serialises typed CDR rows into the CSV shape POST /ingest/cdr
// expects.
func rowsToCDRCSV(rows []map[string]any) string {
	var b strings.Builder
	b.WriteString(strings.Join(cdrColumns, ","))
	for _, row := range rows {
		b.WriteByte('\n')
		fields := make([]string, len(cdrColumns))
		for i, col := range cdrColumns {
			var val any
			for _, key := range cdrAliases[col] {
				if v, ok := row[key]; ok {
					val = v
					break
				}
			}
			fields[i] = cdrField(val)
		}
		b.WriteString(strings.Join(fields, ","))
	}
	return b.String()
}

// postIngest sends a raw body to /ingest?object_type=… and decodes the receipt.
// It honours BulkOptions.DetectPacks via the X-Detect-Packs header.
func (i *IngestClient) postIngest(ctx context.Context, objectType, body, contentType string, opts *BulkOptions) (map[string]any, error) {
	purpose := i.purpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	params := map[string]string{"object_type": objectType}
	if purpose != "" {
		params["purpose"] = purpose
	}
	status, data, _, err := i.c.rawHTTPRequest(ctx, "POST", encodeGetURL("/ingest", params), []byte(body), contentType, i.detectHeader(opts))
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

// postBulkTyped sends rows to POST /ingest/bulk so that OnConflict + TenantID
// (only honoured on the /ingest/bulk door) take effect. Each row is tagged with
// `_type` = objectType as the server's BulkIngestRequest expects.
func (i *IngestClient) postBulkTyped(ctx context.Context, objectType string, rows []map[string]any, opts *BulkOptions) (map[string]any, error) {
	purpose := i.purpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	objects := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		o := make(map[string]any, len(r)+1)
		for k, v := range r {
			o[k] = v
		}
		o["_type"] = objectType
		objects = append(objects, o)
	}
	body := map[string]any{"objects": objects, "on_conflict": opts.OnConflict}
	if purpose != "" {
		body["purpose"] = purpose
	}
	if opts != nil && opts.TenantID != "" {
		body["tenant_id"] = opts.TenantID
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("relata: encode bulk body: %w", err)
	}
	status, data, _, err := i.c.rawHTTPRequest(ctx, "POST", "/ingest/bulk", encoded, "application/json", i.detectHeader(opts))
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

// detectHeader returns a header map carrying X-Detect-Packs when DetectPacks is
// set, else nil (no override).
func (i *IngestClient) detectHeader(opts *BulkOptions) map[string]string {
	if opts != nil && opts.DetectPacks != "" {
		return map[string]string{"X-Detect-Packs": opts.DetectPacks}
	}
	return nil
}
