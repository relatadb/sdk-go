package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestTypedQueryParamsWireAndIndexedError(t *testing.T) {
	const sql = "SELECT id FROM Record WHERE active = $1 AND id IN ($2) AND payload = $3"
	params := []any{true, []any{"safe", "x'); DROP TABLE Record; --", "$1"}, map[string]any{
		"enabled": false, "nested": []any{nil, map[string]any{"label": "'quoted'", "count": float64(7)}},
	}}
	for _, status := range []int{200, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != "POST" || r.URL.Path != "/v1/query" {
					t.Errorf("unexpected route: %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("decode query request: %v", err)
				}
				if status == 400 {
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(400)
					fmt.Fprint(w, `{"type":"about:blank","title":"Invalid query parameter","status":400,"detail":"parameter $2 is unsupported in this context"}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"rows":[],"query_id":"typed","elapsed_ms":1}`)
			}))
			defer srv.Close()
			client := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
			_, err := client.QueryWithParams(context.Background(), sql, params)
			if status == 400 {
				if err == nil || !strings.Contains(err.Error(), "$2") {
					t.Fatalf("expected indexed parameter error, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("query: %v", err)
			}
			if !reflect.DeepEqual(got, map[string]any{"purpose": "analytics", "sql": sql, "params": params}) {
				t.Fatalf("typed request changed: %#v", got)
			}
			if requests != 1 {
				t.Fatalf("requests = %d; typed binding errors must not retry", requests)
			}
		})
	}
}
