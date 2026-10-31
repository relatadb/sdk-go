package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #2252 — CDR + OTLP ingest helpers.

func TestIngestCDR_NormalizesAliasesAndUsesGenericTaskContract(t *testing.T) {
	var gotPath, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		if q := r.URL.Query().Get("purpose"); q != "finint" {
			t.Errorf("purpose = %q, want finint", q)
		}
		if q := r.URL.Query().Get("object_type"); q != "CdrRecord" {
			t.Errorf("object_type = %q, want CdrRecord", q)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"rows_queued":1,"rows_rejected":0,"errors":[],"task_id":"itsk_test"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "finint"})
	ic := NewIngestClient(c)
	rows := []map[string]any{
		{"caller": "911234567890", "callee": "919876543210", "duration": 120, "tower": "CELL-042", "type": "voice", "_pk": "cdr-stable-1", "source_file_id": 42},
		{"a_number": "+91 1111-111111", "b_msisdn": "922222222222", "call_start": 1700000000000000001, "tower_id": "CELL-001", "call_type": "sms", "id": "cdr-stable-2", "source_file_id": 43},
		{"caller": "not a number", "callee": "922222222222"},
	}
	out, err := ic.IngestCDR(context.Background(), rows, &BulkOptions{Purpose: "finint"})
	if err != nil {
		t.Fatalf("IngestCDR: %v", err)
	}
	if rows2, _ := out["rows_queued"].(float64); rows2 != 1 {
		t.Fatalf("rows_queued = %v", out["rows_queued"])
	}
	if gotPath != "/v1/ingest" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotCT != "application/json" {
		t.Fatalf("content-type = %q", gotCT)
	}
	var gotRows []map[string]any
	if err := json.Unmarshal([]byte(gotBody), &gotRows); err != nil {
		t.Fatalf("decode CDR JSON array: %v", err)
	}
	if len(gotRows) != 2 {
		t.Fatalf("JSON-array rows = %d, want 2: %q", len(gotRows), gotBody)
	}
	first, second := gotRows[0], gotRows[1]
	if first["caller_msisdn"] != float64(911234567890) || first["call_type"] != "voice" {
		t.Fatalf("normalized first CDR = %#v", first)
	}
	if first["_pk"] != "cdr-stable-1" || first["source_file_id"] != float64(42) {
		t.Fatalf("first CDR identity/provenance = %#v", first)
	}
	if second["caller_msisdn"] != float64(911111111111) || second["call_type"] != "sms" || second["call_start_ns"] != float64(1700000000000000001) {
		t.Fatalf("normalized second CDR = %#v", second)
	}
	if !strings.Contains(gotBody, `"call_start_ns":1700000000000000001`) {
		t.Fatalf("i64 nanosecond timestamp lost precision on wire: %s", gotBody)
	}
	if second["id"] != "cdr-stable-2" || second["source_file_id"] != float64(43) {
		t.Fatalf("second CDR identity/provenance = %#v", second)
	}
	errors, _ := out["errors"].([]any)
	if len(errors) != 1 || out["rows_rejected"] != float64(1) {
		t.Fatalf("partial row errors = %#v, rejected=%v", errors, out["rows_rejected"])
	}
}

func TestIngestCDR_PreservesOnConflictBulkRoute(t *testing.T) {
	var gotPath, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		if got := r.Header.Get("X-Detect-Packs"); got != "none" {
			t.Errorf("X-Detect-Packs = %q, want none", got)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"rows_queued":1,"rows_rejected":0,"errors":[],"task_id":"itsk_conflict"}`)
	}))
	defer srv.Close()

	ic := NewIngestClient(newTestClient(srv, &ClientOptions{DefaultPurpose: "finint"}))
	_, err := ic.IngestCDR(context.Background(), []map[string]any{{
		"caller": "911234567890", "callee": "919876543210", "_pk": "cdr-stable",
	}}, &BulkOptions{Purpose: "finint", OnConflict: "skip", TenantID: "tenant-a", DetectPacks: "none"})
	if err != nil {
		t.Fatalf("IngestCDR: %v", err)
	}
	if gotPath != "/v1/ingest/bulk" {
		t.Fatalf("path = %q, want /v1/ingest/bulk", gotPath)
	}
	if gotCT != "application/json" {
		t.Fatalf("content-type = %q, want application/json", gotCT)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("decode conflict body: %v", err)
	}
	if body["on_conflict"] != "skip" || body["tenant_id"] != "tenant-a" {
		t.Fatalf("conflict options changed: %#v", body)
	}
	objects, _ := body["objects"].([]any)
	if len(objects) != 1 || objects[0].(map[string]any)["_pk"] != "cdr-stable" {
		t.Fatalf("stable CDR identity missing from conflict route: %#v", objects)
	}
}

func TestNormalizeCDRRowsRejectsCaseCollisionAndKeepsAliasPrecedence(t *testing.T) {
	rows, errors := normalizeCDRRows([]map[string]any{
		{"CALLER": "111", "caller": "222", "callee": "333"},
		{"caller": "111", "caller_msisdn": "222", "callee": "333", "_pk": "stable"},
	})
	if len(errors) != 1 || !strings.Contains(fmt.Sprint(errors[0]), "ambiguous case-insensitive field 'caller'") {
		t.Fatalf("case collision errors = %#v", errors)
	}
	if len(rows) != 1 || rows[0]["caller_msisdn"] != int64(111) || rows[0]["callee_msisdn"] != int64(333) || rows[0]["_pk"] != "stable" {
		t.Fatalf("distinct alias precedence = %#v", rows)
	}
}

func TestCDRTimestampRFC3339NormalizesExactNanosecondsAndRejectsInvalidInputs(t *testing.T) {
	got, err := cdrTimestamp("2024-01-01T01:02:03.123456789+01:30", 0)
	if err != nil || got != 1704065523123456789 {
		t.Fatalf("RFC3339 offset timestamp = %d, %v", got, err)
	}
	for _, input := range []string{
		"2023-02-29T00:00:00Z",
		"2024-01-01T00:00:00.1234567891Z",
		"2500-01-01T00:00:00Z",
		"2024-01-01T00:00:00-00:00",
		"2024-01-01T00:00:00+24:00",
		"2024-01-01T00:00:00+01:60",
	} {
		if _, err := cdrTimestamp(input, 0); err == nil {
			t.Errorf("cdrTimestamp(%q) unexpectedly succeeded", input)
		}
	}
}

func TestCDRKeyFoldIsAsciiOnly(t *testing.T) {
	if cdrASCIIKeyFold("CALLER") != "caller" || cdrASCIIKeyFold("İD") != "İd" {
		t.Fatalf("ASCII-only key folding mismatch: %q", cdrASCIIKeyFold("İD"))
	}
}

func TestOTLP_PostJSONToRightDoor(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if q := r.URL.Query().Get("purpose"); q != "otel" {
			t.Errorf("purpose = %q, want otel", q)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	ic := NewIngestClient(c)
	opts := &BulkOptions{Purpose: "otel"}

	out, err := ic.OTLPTraces(context.Background(), map[string]any{"resourceSpans": []any{}}, opts)
	if err != nil {
		t.Fatalf("OTLPTraces: %v", err)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("response = %v", out)
	}
	if gotPath != "/v1/traces" {
		t.Fatalf("path = %q", gotPath)
	}

	if _, err := ic.OTLPLogs(context.Background(), map[string]any{"resourceLogs": []any{}}, opts); err != nil {
		t.Fatalf("OTLPLogs: %v", err)
	}
	if gotPath != "/v1/logs" {
		t.Fatalf("path = %q", gotPath)
	}

	if _, err := ic.OTLPMetrics(context.Background(), map[string]any{"resourceMetrics": []any{}}, opts); err != nil {
		t.Fatalf("OTLPMetrics: %v", err)
	}
	if gotPath != "/v1/metrics" {
		t.Fatalf("path = %q", gotPath)
	}

	// OTLP bodies are JSON-encoded payloads.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil {
		t.Fatalf("OTLP body not JSON: %v", err)
	}
}

func TestBulkEnvelopeOptionsRouteToBulkDoor(t *testing.T) {
	var gotPath, gotKey, gotDur, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("Idempotency-Key")
		gotDur = r.Header.Get("X-Relata-Durability")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"inserted":1,"errors":[]}`)
	}))
	defer srv.Close()

	ic := NewIngestClient(newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"}))
	expected := ""
	_, err := ic.Bulk(context.Background(), "OrderEvent", []map[string]any{{"_pk": "k1", "qty": 3}}, &BulkOptions{
		IdempotencyKey:   "batch-100",
		Durability:       "sync",
		SourceCheckpoint: &SourceCheckpoint{Source: "kafka/orders-0", Position: "100", ExpectedPosition: &expected},
		Contract:         &ContractRef{Dataset: "orders", Version: 2},
	})
	if err != nil {
		t.Fatalf("Bulk: %v", err)
	}
	if gotPath != "/v1/ingest/bulk" || gotKey != "batch-100" || gotDur != "sync" {
		t.Fatalf("path=%q key=%q durability=%q", gotPath, gotKey, gotDur)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	cp, _ := body["source_checkpoint"].(map[string]any)
	ct, _ := body["contract"].(map[string]any)
	if cp["source"] != "kafka/orders-0" || cp["position"] != "100" || cp["expected_position"] != "" {
		t.Fatalf("source_checkpoint = %#v", cp)
	}
	if ct["dataset"] != "orders" || ct["version"] != float64(2) {
		t.Fatalf("contract = %#v", ct)
	}
	if _, present := body["on_conflict"]; present {
		t.Fatalf("on_conflict must be omitted when unset: %#v", body)
	}
}
