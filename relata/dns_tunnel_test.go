package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnalyzeDNSTunnelRowsPreservesLegacyFields(t *testing.T) {
	rows := []map[string]any{}
	for _, name := range []string{
		"z9q4f8k2m7x1c3v6b0n5h2j8l4p7.exfil.bad.",
		"a1s2d3f4g5h6j7k8l9z0x1c2v3b4.exfil.bad",
		"qwe8rty4uio0pas2dfg6hjk1lzx9.exfil.bad",
		"m4n8b2v6c0x5z3a7s1d9f5g2h6j8.exfil.bad",
		"p3o7i1u5y9t2r6e0w4q8z3x7c1v5.exfil.bad",
		"l9k3j7h1g5f9d3s7a1q5w9e3r7t1.exfil.bad",
	} {
		rows = append(rows, map[string]any{"query_name": name, "qname": "ignored.example"})
	}
	for range 20 {
		rows = append(rows, map[string]any{"query_name": "www.cdn.example"})
	}
	rows = append(rows, map[string]any{"query_name": nil, "qname": "must-not-fall-through.example"})
	result := formatDNSTunnelResult(rows, []string{"DnsLog"}, 3, 5, len(rows), true, nil, "")
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %#v", result.Rows)
	}
	finding := result.Rows[0]
	if finding.Domain != "exfil.bad" || finding.QueryCount != 6 || finding.MaxLabelLen != 28 {
		t.Fatalf("finding = %#v", finding)
	}
	if strings.Join(result.Columns, ",") != "domain,query_count,avg_entropy,max_label_len,confidence" {
		t.Fatalf("columns = %v", result.Columns)
	}
}

func TestDnsTunnelDetectUsesGovernedPagesAndReportsContinuation(t *testing.T) {
	var sqls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/query/schema" {
			_, _ = fmt.Fprint(w, `{"purpose":"security","object_types":["DnsLog","Person"]}`)
			return
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		sql, _ := request["sql"].(string)
		sqls = append(sqls, sql)
		if strings.Contains(sql, "AFTER") {
			_, _ = fmt.Fprint(w, `{"rows":1,"data":[{"query_name":"c.example.com"}],"next_cursor":"200:row-id"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"rows":2,"data":[{"query_name":"a.example.com"},{"query_name":"b.example.com"}],"next_cursor":"100:row-id"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, &ClientOptions{DefaultPurpose: "security"})
	minEntropy := 0.0
	minQueries := uint64(1)
	result, err := c.DnsTunnelDetect(context.Background(), "", &DnsTunnelOptions{
		MinEntropy: &minEntropy, MinQueries: &minQueries, PageSize: 2, MaxRows: 3,
	})
	if err != nil {
		t.Fatalf("DnsTunnelDetect: %v", err)
	}
	if result.Complete || result.IncompleteReason != "max_rows_reached" || result.RowsScanned != 3 {
		t.Fatalf("result = %#v", result)
	}
	if result.Continuation["DnsLog"] != "200:row-id" {
		t.Fatalf("continuation = %#v", result.Continuation)
	}
	if len(sqls) != 2 || !strings.Contains(sqls[0], "WITH CACHE BYPASS") || !strings.Contains(sqls[1], "AFTER '100:row-id'") {
		t.Fatalf("page SQL = %#v", sqls)
	}
}

func TestQueryResultTracksExplicitCursorAndTruncation(t *testing.T) {
	var result QueryResult
	if err := json.Unmarshal([]byte(`{"rows":0,"data":[],"next_cursor":null,"truncated":true,"partial":true}`), &result); err != nil {
		t.Fatal(err)
	}
	if !result.HasNextCursor || result.NextCursor != nil || !result.Truncated || !result.Partial {
		t.Fatalf("result = %#v", result)
	}
}

func TestDnsTunnelDetectRefusesPartialShardResponseAsComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/query/schema" {
			_, _ = fmt.Fprint(w, `{"purpose":"security","object_types":["DnsLog"]}`)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = fmt.Fprint(w, `{"rows":1,"data":[{"query_name":"a.example.com"}],"partial":true}`)
	}))
	defer srv.Close()
	c := New(srv.URL, &ClientOptions{DefaultPurpose: "security"})
	result, err := c.DnsTunnelDetect(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("DnsTunnelDetect: %v", err)
	}
	if result.Complete || result.IncompleteReason != "server_returned_partial_shard_result" {
		t.Fatalf("result = %#v", result)
	}
}
