package relata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// #6778: ExportData decodes /v1/export by the response Content-Type, so a
// one-row jsonl export is text, not the row decoded as the export object.
func exportServer(t *testing.T, contentType, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return newTestClient(srv, nil)
}

func TestExportDataDecodesByContentType(t *testing.T) {
	const csv = "_pk,name,email\np1,Ada,ada@example.com\np2,Bob,bob@example.com\n"
	cases := []struct {
		name, contentType, body, format string
		want                            map[string]any
	}{
		{"csv with rows", "text/csv", csv, "csv",
			map[string]any{"type": "Person", "format": "csv", "content": csv}},
		{"csv empty type", "text/csv", "", "csv",
			map[string]any{"type": "Person", "format": "csv", "content": ""}},
		{"json parsed", "application/json", `{"type":"Person","format":"json","rows":0,"data":[]}`, "json",
			map[string]any{"type": "Person", "format": "json", "rows": float64(0), "data": []any{}}},
		{"stix parsed", "application/stix+json", `{"type":"bundle","objects":[]}`, "stix",
			map[string]any{"type": "bundle", "objects": []any{}}},
		{"one-row jsonl is text", "application/x-ndjson", "{\"_pk\":\"p1\"}\n", "jsonl",
			map[string]any{"type": "Person", "format": "jsonl", "content": "{\"_pk\":\"p1\"}\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := exportServer(t, tc.contentType, tc.body)
			got, err := c.ExportData(context.Background(), "Person", tc.format)
			if err != nil {
				t.Fatalf("ExportData: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDecodeBodyByMediaTypeBinary(t *testing.T) {
	raw := []byte{0, 1}
	got, err := decodeBodyByMediaType("application/octet-stream", raw)
	if err != nil || !reflect.DeepEqual(got, raw) {
		t.Fatalf("got %#v, %v", got, err)
	}
}
