package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// #5666: cross-SDK shared queue-acknowledgement and task-status fixtures (Go leg). See
// sdks/shared/task-receipt-fixtures.json and the Rust live replay test
// (http_e2e::contract_matrix::task_receipt_fixtures_match_live_server) that keeps it faithful.

type taskFixtureCase struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Wire struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"wire"`
	Expect struct {
		TaskID           string   `json:"task_id"`
		Status           string   `json:"status"`
		RowsQueued       int      `json:"rows_queued"`
		RowsRejected     int      `json:"rows_rejected"`
		Errors           []string `json:"errors"`
		QueueDepth       int      `json:"queue_depth"`
		RowsTotal        int      `json:"rows_total"`
		RowsWritten      int      `json:"rows_written"`
		RowsDropped      int      `json:"rows_dropped"`
		DropReasons      []string `json:"drop_reasons"`
		OutcomeUncertain *bool    `json:"outcome_uncertain"`
		Code             *string  `json:"code"`
		DerivedError     *string  `json:"derived_error"`
		Retryable        *bool    `json:"retryable"`
		WriteToken       *struct {
			WallNs  uint64 `json:"wall_ns"`
			Counter uint64 `json:"counter"`
		} `json:"write_token"`
		ExtensionKeys []string `json:"extension_keys"`
	} `json:"expect"`
}

func extensionKeys(m map[string]json.RawMessage) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestTaskReceiptSharedFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "shared", "task-receipt-fixtures.json"))
	if err != nil {
		t.Fatalf("read shared fixtures: %v", err)
	}
	var fx struct {
		Contract string            `json:"contract"`
		Cases    []taskFixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse shared fixtures: %v", err)
	}
	if fx.Contract != "relata.task-receipt-fixtures/v1" || len(fx.Cases) < 8 {
		t.Fatalf("unexpected fixture file: %q with %d cases", fx.Contract, len(fx.Cases))
	}
	for _, c := range fx.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.Wire.Status)
				_, _ = w.Write(c.Wire.Body)
			}))
			defer srv.Close()
			client := newTestClient(srv, &ClientOptions{})
			e := c.Expect
			var ext map[string]json.RawMessage
			if c.Kind == "queue_ack" {
				got, err := client.IngestDocument(context.Background(), []map[string]any{{"_type": "T", "_pk": "1"}}, "analytics")
				if err != nil {
					t.Fatal(err)
				}
				if got.TaskID != e.TaskID || got.RowsQueued != e.RowsQueued || got.RowsRejected != e.RowsRejected ||
					got.QueueDepth != e.QueueDepth || !reflect.DeepEqual(append([]string{}, got.Errors...), append([]string{}, e.Errors...)) {
					t.Fatalf("ack mismatch: %+v want %+v", got, e)
				}
				ext = got.Extensions
			} else {
				got, err := client.IngestDocumentStatus(context.Background(), e.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				// The status string is surfaced verbatim: an unknown value is never coerced.
				if got.TaskID != e.TaskID || got.Status != e.Status || got.RowsTotal != e.RowsTotal ||
					got.RowsWritten != e.RowsWritten || got.RowsDropped != e.RowsDropped {
					t.Fatalf("status mismatch: %+v want %+v", got, e)
				}
				if !reflect.DeepEqual(append([]string{}, got.DropReasons...), append([]string{}, e.DropReasons...)) {
					t.Fatalf("drop reasons %v want %v", got.DropReasons, e.DropReasons)
				}
				if got.OutcomeUncertain != (e.OutcomeUncertain != nil && *e.OutcomeUncertain) {
					t.Fatalf("outcome_uncertain %v want %v", got.OutcomeUncertain, e.OutcomeUncertain)
				}
				if wantCode := optString(e.Code); got.Code != wantCode {
					t.Fatalf("code %q want %q", got.Code, wantCode)
				}
				if wantDerived := optString(e.DerivedError); got.DerivedError != wantDerived {
					t.Fatalf("derived_error %q want %q", got.DerivedError, wantDerived)
				}
				if (got.Retryable == nil) != (e.Retryable == nil) || (got.Retryable != nil && *got.Retryable != *e.Retryable) {
					t.Fatalf("retryable %v want %v", got.Retryable, e.Retryable)
				}
				if (got.WriteToken == nil) != (e.WriteToken == nil) {
					t.Fatalf("write token %v want %v", got.WriteToken, e.WriteToken)
				}
				ext = got.Extensions
			}
			if !reflect.DeepEqual(extensionKeys(ext), append([]string{}, e.ExtensionKeys...)) {
				t.Fatalf("extension keys %v want %v", extensionKeys(ext), e.ExtensionKeys)
			}
		})
	}
}
