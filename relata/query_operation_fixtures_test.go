package relata

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// #5666: cross-SDK shared query-operation fixtures (Go leg). See
// sdks/shared/query-operation-fixtures.json and the Rust live replay test
// (http_e2e::contract_matrix::query_operation_fixtures_match_live_server) that keeps it faithful.

type queryOpCase struct {
	Name string `json:"name"`
	Call struct {
		SQL              string    `json:"sql"`
		Purpose          string    `json:"purpose"`
		QueryVector      []float64 `json:"query_vector"`
		QueryVectorModel string    `json:"query_vector_model"`
	} `json:"call"`
	Request struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	} `json:"request"`
	Wire struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"wire"`
	Expect struct {
		Rows              []map[string]any `json:"rows"`
		Columns           []string         `json:"columns"`
		RowCount          int              `json:"row_count"`
		QueryID           string           `json:"query_id"`
		NextCursorPresent bool             `json:"next_cursor_present"`
		NextCursor        *string          `json:"next_cursor"`
		Truncated         bool             `json:"truncated"`
	} `json:"expect"`
}

func TestQueryOperationSharedFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "query-operation-fixtures.json"))
	if err != nil {
		t.Fatalf("read shared fixtures: %v", err)
	}
	var fx struct {
		Contract string        `json:"contract"`
		Cases    []queryOpCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse shared fixtures: %v", err)
	}
	if fx.Contract != "relata.query-operation-fixtures/v1" || len(fx.Cases) < 4 {
		t.Fatalf("unexpected fixture file: %q with %d cases", fx.Contract, len(fx.Cases))
	}
	for _, c := range fx.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.Wire.Status)
				_, _ = w.Write(c.Wire.Body)
			}))
			defer srv.Close()
			client := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
			opts := []QueryOption{WithPurpose(c.Call.Purpose)}
			if c.Call.QueryVector != nil {
				opts = append(opts, WithQueryVector(c.Call.QueryVector, c.Call.QueryVectorModel))
			}
			got, err := client.Query(context.Background(), c.Call.SQL, opts...)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			var wantBody any
			if err := json.Unmarshal(c.Request.Body, &wantBody); err != nil {
				t.Fatal(err)
			}
			if gotMethod != c.Request.Method || gotPath != c.Request.Path {
				t.Fatalf("request %s %s want %s %s", gotMethod, gotPath, c.Request.Method, c.Request.Path)
			}
			if !reflect.DeepEqual(gotBody, wantBody) {
				t.Fatalf("request body %v want %v", gotBody, wantBody)
			}
			e := c.Expect
			if len(got.Rows) != len(e.Rows) || (len(e.Rows) > 0 && !reflect.DeepEqual(got.Rows, e.Rows)) {
				t.Fatalf("rows %v want %v", got.Rows, e.Rows)
			}
			if len(got.Columns) != len(e.Columns) || (len(e.Columns) > 0 && !reflect.DeepEqual(got.Columns, e.Columns)) {
				t.Fatalf("columns %v want %v", got.Columns, e.Columns)
			}
			if got.RowCount != e.RowCount || got.QueryID != e.QueryID {
				t.Fatalf("row_count %d query_id %q want %d %q", got.RowCount, got.QueryID, e.RowCount, e.QueryID)
			}
			if got.HasNextCursor != e.NextCursorPresent {
				t.Fatalf("next_cursor present %v want %v", got.HasNextCursor, e.NextCursorPresent)
			}
			if (got.NextCursor == nil) != (e.NextCursor == nil) || (e.NextCursor != nil && *got.NextCursor != *e.NextCursor) {
				t.Fatalf("next_cursor %v want %v", got.NextCursor, e.NextCursor)
			}
			if got.Truncated != e.Truncated {
				t.Fatalf("truncated %v want %v", got.Truncated, e.Truncated)
			}
		})
	}
}
