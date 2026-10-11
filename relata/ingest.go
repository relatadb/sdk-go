package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// IngestClient exposes the generic row-ingest APIs and media-upload status
// polling. Client.IngestDocument sends prepared document rows as a queued
// mixed-type request to /ingest/bulk; document extraction and source-format
// mapping belong in an application adapter.
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

// IngestCDR normalizes CDR aliases and queues canonical CdrRecord rows through
// the generic POST /ingest task contract. Invalid source rows are returned in
// the receipt's errors; poll task_id to observe writer completion.
func (i *IngestClient) IngestCDR(ctx context.Context, rows []map[string]any, opts *BulkOptions) (map[string]any, error) {
	normalized, rowErrors := normalizeCDRRows(rows)
	var resp map[string]any
	var err error
	if len(normalized) > 0 {
		resp, err = i.Bulk(ctx, "CdrRecord", normalized, opts)
	} else {
		resp, err = i.postIngest(ctx, "CdrRecord", "[]", "application/json", opts)
	}
	if err != nil {
		return nil, err
	}
	serverErrors, _ := resp["errors"].([]any)
	resp["errors"] = append(rowErrors, serverErrors...)
	rejected, _ := resp["rows_rejected"].(float64)
	resp["rows_rejected"] = rejected + float64(len(rowErrors))
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

// ── CDR application adapter (#5744) ──────────────────────────────────────────

// cdrAliases maps canonical CDR fields to accepted input aliases.
var cdrAliases = map[string][]string{
	"caller":   {"caller", "caller_msisdn", "a_number", "a_msisdn"},
	"callee":   {"callee", "callee_msisdn", "b_number", "b_msisdn", "called"},
	"start":    {"start", "call_start", "start_time", "timestamp", "call_start_ns"},
	"duration": {"duration", "duration_secs", "duration_seconds"},
	"tower":    {"tower", "tower_id", "cell_id", "site", "cell"},
	"type":     {"type", "call_type", "record_type"},
}

func normalizeCDRRows(rows []map[string]any) ([]map[string]any, []any) {
	normalized := make([]map[string]any, 0, len(rows))
	errors := make([]any, 0)
	nowNs := time.Now().UnixNano()
	for index, row := range rows {
		value := func(field string) any {
			for _, alias := range cdrAliases[field] {
				for key, item := range row {
					if strings.EqualFold(key, alias) {
						return item
					}
				}
			}
			return nil
		}
		callerRaw, calleeRaw := value("caller"), value("callee")
		callerDigits, calleeDigits := cdrDigits(callerRaw), cdrDigits(calleeRaw)
		fail := ""
		if len(callerDigits) > 15 {
			fail = fmt.Sprintf("caller_msisdn '%v': too many digits (max 15 for E.164)", callerRaw)
		} else if len(calleeDigits) > 15 {
			fail = fmt.Sprintf("callee_msisdn '%v': too many digits (max 15 for E.164)", calleeRaw)
		}
		caller, callerErr := strconv.ParseInt(callerDigits, 10, 64)
		callee, calleeErr := strconv.ParseInt(calleeDigits, 10, 64)
		if fail == "" && callerErr != nil {
			fail = fmt.Sprintf("caller_msisdn '%s': %v", callerDigits, callerErr)
		}
		if fail == "" && calleeErr != nil {
			fail = fmt.Sprintf("callee_msisdn '%s': %v", calleeDigits, calleeErr)
		}
		start := nowNs
		if raw := value("start"); raw != nil {
			parsed, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(raw)), 10, 64)
			if err != nil && fail == "" {
				fail = fmt.Sprintf("call_start '%v': %v", raw, err)
			} else if err == nil {
				start = parsed
				if start < 10_000_000_000_000 {
					if start > int64(^uint64(0)>>1)/1_000_000_000 {
						start = int64(^uint64(0) >> 1)
					} else if start < (-int64(^uint64(0)>>1)-1)/1_000_000_000 {
						start = -int64(^uint64(0)>>1) - 1
					} else {
						start *= 1_000_000_000
					}
				}
			}
		}
		duration := uint64(0)
		if raw := value("duration"); raw != nil {
			parsed, err := strconv.ParseUint(strings.TrimSpace(fmt.Sprint(raw)), 10, 32)
			if err != nil && fail == "" {
				fail = fmt.Sprintf("duration '%v': %v", raw, err)
			} else if err == nil {
				duration = parsed
			}
		}
		if fail != "" {
			errors = append(errors, map[string]any{"row": index + 1, "error": fail})
			continue
		}
		callType := strings.ToLower(strings.TrimSpace(fmt.Sprint(value("type"))))
		switch callType {
		case "voice", "call", "0":
			callType = "voice"
		case "sms", "mms", "1":
			callType = "sms"
		case "data", "gprs", "2":
			callType = "data"
		default:
			callType = "unknown"
		}
		tower := ""
		if raw := value("tower"); raw != nil {
			tower = strings.TrimSpace(fmt.Sprint(raw))
		}
		normalizedRow := map[string]any{
			"caller_msisdn": caller, "callee_msisdn": callee,
			"call_start_ns": start, "duration_secs": duration,
			"tower_id": tower, "call_type": callType, "source_file_id": 0,
		}
		for _, field := range []string{"_pk", "id", "source_file_id"} {
			if raw, ok := cdrMetadataValue(row, field); ok && raw != nil {
				normalizedRow[field] = raw
			}
		}
		normalized = append(normalized, normalizedRow)
	}
	return normalized, errors
}

func cdrMetadataValue(row map[string]any, field string) (any, bool) {
	if value, ok := row[field]; ok {
		return value, true
	}
	for key, value := range row {
		if strings.EqualFold(key, field) {
			return value, true
		}
	}
	return nil, false
}

func cdrDigits(value any) string {
	text := fmt.Sprint(value)
	var digits strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	return digits.String()
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
