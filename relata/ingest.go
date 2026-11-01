package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IngestClient exposes the generic row-ingest APIs and media-upload status
// polling. Client.IngestDocument sends prepared document rows as a queued
// mixed-type request to /v1/ingest/bulk; document extraction and source-format
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
	// call to POST /v1/ingest/bulk (mixed-type JSON) instead of /v1/ingest. One of
	// "upsert" | "skip" | "error". Empty = default server behaviour.
	OnConflict string
	// TenantID optionally scopes the write to a tenant (forwarded to the
	// governance layer). Mirrors BulkIngestRequest.tenant_id.
	TenantID string
	// IdempotencyKey sets the Idempotency-Key header (#5689): a retry with the
	// same key and rows replays the recorded response without a second write; a
	// different body is 409 idempotency-conflict. Routes the call to /v1/ingest/bulk.
	IdempotencyKey string
	// Durability "sync" sends X-Relata-Durability: sync (WAL fsynced before the
	// response).
	Durability string
	// SourceCheckpoint asks the server to advance a durable source checkpoint
	// only after a successful synchronous write (requires Durability "sync").
	// Routes the call to /v1/ingest/bulk.
	SourceCheckpoint *SourceCheckpoint
	// Contract validates rows against a data contract (#5714); invalid rows are
	// quarantined server-side. Routes the call to /v1/ingest/bulk.
	Contract *ContractRef
	// MediaWorkClaim is the claim_id from MediaWorkClient.Claim, sent as
	// X-Relata-Claim (0 = not sent). Required when SourceCheckpoint names an
	// openetl/media/<work_id> source; the server fences stale holders. Routes
	// the call to /v1/ingest/bulk.
	MediaWorkClaim uint64
}

// SourceCheckpoint is the bulk-ingest source_checkpoint member (#5689).
type SourceCheckpoint struct {
	Source   string `json:"source"`
	Position string `json:"position"`
	// ExpectedPosition is a compare-and-set guard ("" pointer = not sent).
	ExpectedPosition *string `json:"expected_position,omitempty"`
}

// ContractRef is the bulk-ingest contract member (#5714).
type ContractRef struct {
	Dataset string `json:"dataset"`
	Version uint32 `json:"version,omitempty"`
}

// Bulk bulk-ingests rows as NDJSON. Returns the server receipt.
// Local encoding failure rejects the entire request before HTTP and identifies
// the 1-based row; a valid prefix is never sent as an apparent successful batch.
func (i *IngestClient) Bulk(ctx context.Context, objectType string, rows []map[string]any, opts *BulkOptions) (map[string]any, error) {
	if opts != nil && (opts.OnConflict != "" || opts.IdempotencyKey != "" ||
		opts.SourceCheckpoint != nil || opts.Contract != nil || opts.MediaWorkClaim != 0) {
		// Route to /v1/ingest/bulk so on_conflict + tenant_id + the envelope take effect
		// (the /v1/ingest door doesn't accept them).
		return i.postBulkTyped(ctx, objectType, rows, opts)
	}
	body, err := rowToNDJSON(rows)
	if err != nil {
		return nil, err
	}
	return i.postIngest(ctx, objectType, body, "application/x-ndjson", opts)
}

// BulkCSV bulk-ingests a CSV string. The server parses it server-side.
func (i *IngestClient) BulkCSV(ctx context.Context, objectType, csvText string, opts *BulkOptions) (map[string]any, error) {
	return i.postIngest(ctx, objectType, csvText, "text/csv", opts)
}

// AutoOptions configures schemaless ingest (POST /v1/ingest/auto, T6 #1988).
type AutoOptions struct {
	// Purpose overrides the parent client's default purpose.
	Purpose string
	// Schema overrides field-type inference, keyed by field name.
	Schema map[string]string
	// TenantID scopes the write to a tenant.
	TenantID string
}

// IngestAuto is a schemaless ingest — the type is auto-created on first write
// with inferred fields (T6, #1988). Wraps POST /v1/ingest/auto. The response
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
	status, data, _, err := i.c.rawHTTPRequest(ctx, "POST", "/v1/ingest/auto", encoded, "application/json")
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

// IngestIter streams rows from an iterator channel into batched POST /v1/ingest
// calls with backpressure. Memory is O(batchSize). The count is input rows in
// batches whose request returned without an SDK error, not a writer-confirmed
// count: /v1/ingest is queued. This helper discards batch receipts/task IDs and
// does not inspect row-level errors in successful responses. Call Bulk per
// batch and poll each task when completion or per-row error accounting is
// required. On error, the returned count covers only earlier successful HTTP
// calls; the failing batch is excluded.
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

// IngestCDR normalizes CDR aliases and queues canonical CdrRecord rows through
// the generic POST /v1/ingest task contract. Invalid source rows are returned in
// the receipt's errors; poll task_id to observe writer completion.
func (i *IngestClient) IngestCDR(ctx context.Context, rows []map[string]any, opts *BulkOptions) (map[string]any, error) {
	normalized, rowErrors := normalizeCDRRows(rows)
	var resp map[string]any
	var err error
	if len(normalized) > 0 {
		if opts != nil && opts.OnConflict != "" {
			// Preserve Bulk's /v1/ingest/bulk contract when callers request conflict
			// handling. The plain /v1/ingest JSON-array path has no such option.
			resp, err = i.Bulk(ctx, "CdrRecord", normalized, opts)
		} else {
			body, encodeErr := json.Marshal(normalized)
			if encodeErr != nil {
				return nil, fmt.Errorf("relata: encode CDR rows: %w", encodeErr)
			}
			resp, err = i.postIngest(ctx, "CdrRecord", string(body), "application/json", opts)
		}
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

// ImportSTIX imports a STIX 2.1 bundle via POST /v1/ingest/import?format=stix, the
// mirror image of GET /v1/export?format=stix. Per-type guard failures are
// reported in the response rather than failing the whole bundle.
func (i *IngestClient) ImportSTIX(ctx context.Context, bundle map[string]any, opts *BulkOptions) (map[string]any, error) {
	params := map[string]string{"format": "stix", "purpose": i.effectivePurpose(opts)}
	var resp map[string]any
	if err := i.c.postJSON(ctx, encodeGetURL("/v1/ingest/import", params), bundle, &resp); err != nil {
		return nil, err
	}
	return resp, nil
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
		keys := make([]string, 0, len(row))
		for key := range row {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		seenKeys := make(map[string]struct{}, len(row))
		caseCollision := ""
		for _, key := range keys {
			folded := cdrASCIIKeyFold(key)
			if _, exists := seenKeys[folded]; exists {
				caseCollision = folded
				break
			}
			seenKeys[folded] = struct{}{}
		}
		if caseCollision != "" {
			errors = append(errors, map[string]any{"row": index + 1, "error": fmt.Sprintf("ambiguous case-insensitive field '%s'", caseCollision)})
			continue
		}
		value := func(field string) any {
			for _, alias := range cdrAliases[field] {
				for key, item := range row {
					if cdrASCIIKeyFold(key) == alias {
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
		start, startErr := cdrTimestamp(value("start"), nowNs)
		if startErr != nil && fail == "" {
			fail = startErr.Error()
		}
		duration := uint64(0)
		if raw := value("duration"); raw != nil {
			parsed, err := strconv.ParseUint(strings.TrimSpace(cdrText(raw)), 10, 32)
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
		callType := strings.ToLower(strings.TrimSpace(cdrText(value("type"))))
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
			tower = strings.TrimSpace(cdrText(raw))
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

// CDR aliases are ASCII identifiers. Fold only ASCII A-Z so duplicate-key
// handling has identical semantics in the Python, TypeScript, and Go SDKs.
func cdrASCIIKeyFold(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

var cdrRfc3339 = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})$`)

func cdrTimestamp(raw any, nowNs int64) (int64, error) {
	if raw == nil {
		return nowNs, nil
	}
	text := strings.TrimSpace(cdrText(raw))
	if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
		if parsed < 10_000_000_000_000 {
			const billion = int64(1_000_000_000)
			if parsed > int64(^uint64(0)>>1)/billion {
				return int64(^uint64(0) >> 1), nil
			}
			if parsed < (-int64(^uint64(0)>>1)-1)/billion {
				return -int64(^uint64(0)>>1) - 1, nil
			}
			parsed *= billion
		}
		return parsed, nil
	}
	if _, ok := raw.(string); !ok {
		return 0, fmt.Errorf("call_start '%s': invalid integer or RFC3339 timestamp", text)
	}
	parts := cdrRfc3339.FindStringSubmatch(text)
	if parts == nil {
		return 0, fmt.Errorf("call_start '%s': expected integer epoch or RFC3339 timestamp", text)
	}
	if len(parts[2]) > 9 {
		return 0, fmt.Errorf("call_start RFC3339 fraction exceeds nanosecond precision")
	}
	if parts[3] == "-00:00" {
		return 0, fmt.Errorf("call_start RFC3339 -00:00 offset is unknown, not UTC")
	}
	if parts[3] != "Z" {
		offsetHour, _ := strconv.Atoi(parts[3][1:3])
		offsetMinute, _ := strconv.Atoi(parts[3][4:6])
		if offsetHour > 23 || offsetMinute > 59 {
			return 0, fmt.Errorf("call_start '%s': invalid RFC3339 offset", text)
		}
	}
	parsed, err := time.Parse(time.RFC3339, parts[1]+parts[3])
	if err != nil {
		return 0, fmt.Errorf("call_start '%s': invalid RFC3339 date, time, or offset", text)
	}
	if parts[2] != "" {
		parsed, err = time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return 0, fmt.Errorf("call_start '%s': invalid RFC3339 date, time, or offset", text)
		}
	}
	value := new(big.Int).Mul(big.NewInt(parsed.Unix()), big.NewInt(1_000_000_000))
	value.Add(value, big.NewInt(int64(parsed.Nanosecond())))
	if !value.IsInt64() {
		return 0, fmt.Errorf("call_start RFC3339 value is outside i64 nanosecond range")
	}
	return value.Int64(), nil
}

func cdrMetadataValue(row map[string]any, field string) (any, bool) {
	if value, ok := row[field]; ok {
		return value, true
	}
	for key, value := range row {
		if cdrASCIIKeyFold(key) == field {
			return value, true
		}
	}
	return nil, false
}

// cdrText renders a source value as the text the alias parsers expect. JSON
// decoding into map[string]any yields float64 for every number, and fmt.Sprint
// renders those in exponent form ("1.7e+09"), which would be rejected as a
// timestamp or, worse, have its exponent digits folded into an MSISDN.
func cdrText(value any) string {
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	default:
		return fmt.Sprint(value)
	}
}

func cdrDigits(value any) string {
	text := cdrText(value)
	var digits strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	return digits.String()
}

// postIngest sends a raw body to /v1/ingest?object_type=… and decodes the receipt.
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
	status, data, _, err := i.c.rawHTTPRequest(ctx, "POST", encodeGetURL("/v1/ingest", params), []byte(body), contentType, i.detectHeader(opts))
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

// postBulkTyped sends rows to POST /v1/ingest/bulk so that OnConflict + TenantID
// (only honoured on the /v1/ingest/bulk door) take effect. Each row is tagged with
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
	body := map[string]any{"objects": objects}
	if opts.OnConflict != "" {
		body["on_conflict"] = opts.OnConflict
	}
	if purpose != "" {
		body["purpose"] = purpose
	}
	if opts.TenantID != "" {
		body["tenant_id"] = opts.TenantID
	}
	if opts.SourceCheckpoint != nil {
		body["source_checkpoint"] = opts.SourceCheckpoint
	}
	if opts.Contract != nil {
		body["contract"] = opts.Contract
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("relata: encode bulk body: %w", err)
	}
	status, data, _, err := i.c.rawHTTPRequest(ctx, "POST", "/v1/ingest/bulk", encoded, "application/json", i.detectHeader(opts))
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
	if opts == nil {
		return nil
	}
	var h map[string]string
	set := func(k, v string) {
		if h == nil {
			h = map[string]string{}
		}
		h[k] = v
	}
	if opts.DetectPacks != "" {
		set("X-Detect-Packs", opts.DetectPacks)
	}
	if opts.IdempotencyKey != "" {
		set("Idempotency-Key", opts.IdempotencyKey)
	}
	if opts.Durability != "" {
		set("X-Relata-Durability", opts.Durability)
	}
	if opts.MediaWorkClaim != 0 {
		set(MediaWorkClaimHeader, strconv.FormatUint(opts.MediaWorkClaim, 10))
	}
	return h
}
