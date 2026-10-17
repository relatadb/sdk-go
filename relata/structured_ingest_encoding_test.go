package relata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStructuredIngestEncodingErrorsRefuseAllRowsBeforeHTTP(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	invalid := []struct {
		name  string
		value any
	}{
		{"nan", math.NaN()}, {"infinity", math.Inf(1)}, {"function", func() {}}, {"cycle", cycle},
	}
	for _, bad := range invalid {
		t.Run(bad.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, `{"rows_queued":2,"task_id":"must-not-be-created"}`)
			}))
			defer server.Close()
			client := newTestClient(server, &ClientOptions{DefaultPurpose: "analytics", Tenant: "tenant-a"})
			defer client.Close()
			rows := []map[string]any{{"id": "prefix"}, {"id": "bad", "value": bad.value}, {"id": "suffix"}}
			for _, call := range []struct {
				name string
				run  func() (map[string]any, error)
				row  string
			}{
				{"Bulk", func() (map[string]any, error) {
					return NewIngestClient(client).Bulk(context.Background(), "StructuredRecord", rows, nil)
				}, "row 2"},
				{"BatchUpsert", func() (map[string]any, error) {
					return NewObjectClient(client).BatchUpsert(context.Background(), "StructuredRecord", rows, nil)
				}, "row 2"},
				{"Upsert", func() (map[string]any, error) {
					return NewObjectClient(client).Upsert(context.Background(), "StructuredRecord", "bad", rows[1], nil)
				}, "row 1"},
			} {
				t.Run(call.name, func(t *testing.T) {
					receipt, err := call.run()
					if err == nil || receipt != nil || !strings.Contains(err.Error(), call.row) {
						t.Fatalf("receipt=%v err=%v", receipt, err)
					}
					var unsupportedValue *json.UnsupportedValueError
					var unsupportedType *json.UnsupportedTypeError
					if !errors.As(err, &unsupportedValue) && !errors.As(err, &unsupportedType) {
						t.Fatalf("encoding cause lost: %v", err)
					}
					if requests.Load() != 0 {
						t.Fatalf("encoding failure sent %d HTTP mutations", requests.Load())
					}
				})
			}
		})
	}
}

func TestStructuredIngestEncodingPreservesExactValuesAndTenant(t *testing.T) {
	expectedTime := "2026-10-12T14:37:59.123456789+05:30"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/ingest" || r.URL.Query().Get("object_type") != "StructuredRecord" || r.URL.Query().Get("purpose") != "analytics" {
			t.Errorf("routing=%s", r.URL.String())
		}
		if r.Header.Get("X-Relata-Tenant-Id") != "tenant-a" {
			t.Errorf("tenant=%q", r.Header.Get("X-Relata-Tenant-Id"))
		}
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		var got map[string]any
		if err := decoder.Decode(&got); err != nil {
			t.Error(err)
			return
		}
		if got["id"] != "source:key" || got["_pk"] != "source:key" || got["exact"] != json.Number("9007199254740993") || got["max"] != json.Number("9223372036854775807") || got["decimal"] != "12345678901234567890.123456789" || got["time"] != expectedTime || got["null"] != nil {
			t.Errorf("wire values=%v", got)
		}
		if _, ok := got["null"]; !ok {
			t.Error("null field omitted")
		}
		if err := decoder.Decode(&got); err != io.EOF {
			t.Errorf("unexpected trailing rows: %v", err)
		}
		_, _ = fmt.Fprint(w, `{"rows_queued":1,"task_id":"queued-not-durable"}`)
	}))
	defer server.Close()
	client := newTestClient(server, &ClientOptions{DefaultPurpose: "analytics", Tenant: "tenant-a"})
	defer client.Close()
	row := map[string]any{"id": "source:key", "_pk": "source:key", "exact": int64(9007199254740993), "max": json.Number("9223372036854775807"), "decimal": "12345678901234567890.123456789", "time": expectedTime, "null": nil}
	receipt, err := NewIngestClient(client).Bulk(context.Background(), "StructuredRecord", []map[string]any{row}, nil)
	if err != nil || receipt["task_id"] != "queued-not-durable" || calls.Load() != 1 {
		t.Fatalf("receipt=%v err=%v calls=%d", receipt, err, calls.Load())
	}
}
