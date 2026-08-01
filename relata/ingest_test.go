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

func TestIngestCDR_SerialisesCSVWithAliases(t *testing.T) {
	var gotPath, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		if q := r.URL.Query().Get("purpose"); q != "finint" {
			t.Errorf("purpose = %q, want finint", q)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"rows_queued":2,"errors":[],"queue_depth":0}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "finint"})
	ic := NewIngestClient(c)
	rows := []map[string]any{
		{"caller": "911234567890", "callee": "919876543210", "duration": 120, "tower": "CELL-042", "type": "voice"},
		{"a_number": "911111111111", "b_number": "922222222222", "tower_id": "CELL-001", "call_type": "sms"},
	}
	out, err := ic.IngestCDR(context.Background(), rows, &BulkOptions{Purpose: "finint"})
	if err != nil {
		t.Fatalf("IngestCDR: %v", err)
	}
	if rows2, _ := out["rows_queued"].(float64); rows2 != 2 {
		t.Fatalf("rows_queued = %v", out["rows_queued"])
	}
	if gotPath != "/ingest/cdr" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotCT != "text/csv" {
		t.Fatalf("content-type = %q", gotCT)
	}
	lines := strings.Split(gotBody, "\n")
	if lines[0] != "caller,callee,start,duration,tower,type" {
		t.Fatalf("header = %q", lines[0])
	}
	// Aliases resolve to canonical columns.
	if !strings.Contains(gotBody, "911234567890") || !strings.Contains(gotBody, "911111111111") {
		t.Fatalf("body missing MSISDNs: %q", gotBody)
	}
	if !strings.Contains(gotBody, "CELL-042") {
		t.Fatalf("body missing tower id: %q", gotBody)
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
