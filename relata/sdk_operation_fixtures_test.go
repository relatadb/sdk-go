package relata

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// #5666: cross-SDK shared operation fixtures for objects, search, memory and assertions (Go leg).
// See sdks/shared/sdk-operation-fixtures.json and the Rust live replay test
// (http_e2e::contract_matrix::sdk_operation_fixtures_match_live_server) that keeps it faithful.

type opRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   map[string]string `json:"query"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
	NDJSON  []json.RawMessage `json:"ndjson"`
}

type opCase struct {
	Name   string   `json:"name"`
	Family string   `json:"family"`
	SDKs   []string `json:"sdks"`
	Call   struct {
		Op   string         `json:"op"`
		Args map[string]any `json:"args"`
	} `json:"call"`
	Request         opRequest            `json:"request"`
	RequestOverride map[string]opRequest `json:"request_override"`
	Wire            struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"wire"`
	Expect struct {
		Result any `json:"result"`
		Error  *struct {
			Status         int    `json:"status"`
			Code           string `json:"code"`
			CurrentVersion *int64 `json:"current_version"`
		} `json:"error"`
	} `json:"expect"`
	ExpectOverride map[string]struct {
		Result any `json:"result"`
	} `json:"expect_override"`
}

func argStr(a map[string]any, k string) string { s, _ := a[k].(string); return s }
func argInt(a map[string]any, k string) int    { f, _ := a[k].(float64); return int(f) }
func argVer(a map[string]any) *int64 {
	f, ok := a["expected_version"].(float64)
	if !ok {
		return nil
	}
	v := int64(f)
	return &v
}

// normalise round-trips a Go value through JSON so it compares with the fixture's decoded JSON.
func normalise(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func dispatchOp(t *testing.T, srv *httptest.Server, c opCase) (any, error) {
	t.Helper()
	ctx := context.Background()
	a := c.Call.Args
	client := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics", BearerToken: "t"})
	asMap := func(out map[string]any, err error) (any, error) {
		if err != nil || out == nil {
			return nil, err
		}
		return normalise(t, out), nil
	}
	switch c.Call.Op {
	case "objects.put", "objects.upsert_ingest":
		o := NewObjectClient(client)
		var opts *UpsertOptions
		if c.Call.Op == "objects.put" {
			opts = &UpsertOptions{ExpectedVersion: argVer(a)}
			opts.IfAbsent, _ = a["if_absent"].(bool)
		}
		fields, _ := a["fields"].(map[string]any)
		return asMap(o.Upsert(ctx, argStr(a, "object_type"), argStr(a, "object_id"), fields, opts))
	case "objects.get":
		return asMap(NewObjectClient(client).Get(ctx, argStr(a, "object_type"), argStr(a, "object_id")))
	case "objects.delete":
		return asMap(NewObjectClient(client).Delete(ctx, argStr(a, "object_type"), argStr(a, "object_id"),
			&DeleteOptions{ExpectedVersion: argVer(a)}))
	case "search.query":
		opts := []SearchOption{}
		if _, ok := a["limit"]; ok {
			opts = append(opts, WithSearchLimit(argInt(a, "limit")))
		}
		if h, _ := a["highlight"].(bool); h {
			opts = append(opts, WithHighlight())
		}
		r, err := client.Search(ctx, argStr(a, "query"), argStr(a, "type"), opts...)
		if err != nil {
			return nil, err
		}
		ids, fields, scores, spans := []any{}, []any{}, []any{}, []any{}
		for _, h := range r.Hits {
			ids = append(ids, h.ID)
			f := h.Fields
			if f == nil {
				f = map[string]any{}
			}
			fields = append(fields, f)
			scores = append(scores, h.Score)
			sp := h.HighlightSpans
			if sp == nil {
				sp = []SearchHighlightSpan{}
			}
			spans = append(spans, sp)
		}
		return normalise(t, map[string]any{
			"hit_ids": ids, "hit_fields": fields, "hit_scores": scores, "hit_highlight_spans": spans,
			"total": r.Total, "estimated_total_hits": r.EstimatedTotalHits,
		}), nil
	case "memory.add", "memory.search", "memory.get":
		m, err := NewMemory(srv.URL, "analytics", &MemoryOptions{BearerToken: "t"})
		if err != nil {
			return nil, err
		}
		switch c.Call.Op {
		case "memory.add":
			opts := []MemoryOption{}
			if _, ok := a["confidence"]; ok {
				opts = append(opts, WithConfidence(a["confidence"].(float64)))
			}
			if _, ok := a["memory_class"]; ok {
				opts = append(opts, WithMemoryClass(argStr(a, "memory_class")))
			}
			id, err := m.Add(ctx, argStr(a, "content"), opts...)
			return id, err
		case "memory.search":
			opts := []MemoryOption{}
			if _, ok := a["top_k"]; ok {
				opts = append(opts, WithTopK(argInt(a, "top_k")))
			}
			rows, err := m.Search(ctx, argStr(a, "query"), opts...)
			if err != nil {
				return nil, err
			}
			return normalise(t, rows), nil
		default:
			return asMap(m.Get(ctx, argStr(a, "memory_id")))
		}
	case "assertions.record", "assertions.get", "assertions.list", "assertions.confirm", "assertions.revoke":
		s := NewAssertionsClient(client)
		switch c.Call.Op {
		case "assertions.record":
			asr, _ := a["assertion"].(map[string]any)
			return asMap(s.Record(ctx, asr))
		case "assertions.get":
			return asMap(s.Get(ctx, argStr(a, "id")))
		case "assertions.list":
			return asMap(s.List(ctx, argStr(a, "subject_type"), argStr(a, "subject_id"),
				argStr(a, "predicate"), argStr(a, "state"), argInt(a, "limit")))
		case "assertions.confirm":
			return asMap(s.Confirm(ctx, argStr(a, "id"), argStr(a, "reason")))
		default:
			return asMap(s.Revoke(ctx, argStr(a, "id"), argStr(a, "reason")))
		}
	}
	t.Fatalf("fixture op %s has no Go dispatch", c.Call.Op)
	return nil, nil
}

func TestSDKOperationSharedFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "shared", "sdk-operation-fixtures.json"))
	if err != nil {
		t.Fatalf("read shared fixtures: %v", err)
	}
	var fx struct {
		Contract string   `json:"contract"`
		SDKs     []string `json:"sdks"`
		Cases    []opCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse shared fixtures: %v", err)
	}
	if fx.Contract != "relata.sdk-operation-fixtures/v1" {
		t.Fatalf("unexpected contract %q", fx.Contract)
	}
	families := map[string]bool{}
	for _, c := range fx.Cases {
		families[c.Family] = true
	}
	if len(families) != 4 {
		t.Fatalf("families = %v, want objects, search, memory, assertions", families)
	}
	for _, c := range fx.Cases {
		c := c
		sdks := c.SDKs
		if sdks == nil {
			sdks = fx.SDKs
		}
		in := false
		for _, s := range sdks {
			in = in || s == "go"
		}
		if !in {
			continue
		}
		t.Run(c.Family+"/"+c.Name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotQuery map[string]string
			var gotHeaders http.Header
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotHeaders = r.Method, r.URL.Path, r.Header
				gotQuery = map[string]string{}
				for k, v := range r.URL.Query() {
					gotQuery[k] = v[0]
				}
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				if c.Wire.Status >= 400 {
					w.Header().Set("Content-Type", "application/problem+json")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(c.Wire.Status)
				_, _ = w.Write(c.Wire.Body)
			}))
			defer srv.Close()

			got, callErr := dispatchOp(t, srv, c)

			want := c.Request
			if o, ok := c.RequestOverride["go"]; ok {
				if o.Body != nil {
					want.Body = o.Body
				}
				if o.Path != "" {
					want.Path = o.Path
				}
			}
			if gotMethod != want.Method || gotPath != want.Path {
				t.Fatalf("request %s %s want %s %s", gotMethod, gotPath, want.Method, want.Path)
			}
			wantQuery := want.Query
			if wantQuery == nil {
				wantQuery = map[string]string{}
			}
			if !reflect.DeepEqual(gotQuery, wantQuery) {
				t.Fatalf("query %v want %v", gotQuery, wantQuery)
			}
			for k, v := range want.Headers {
				if gotHeaders.Get(k) != v {
					t.Fatalf("header %s = %q want %q", k, gotHeaders.Get(k), v)
				}
			}
			switch {
			case want.NDJSON != nil:
				var gotLines []any
				for _, l := range strings.Split(strings.TrimRight(gotBody, "\n"), "\n") {
					var v any
					if err := json.Unmarshal([]byte(l), &v); err != nil {
						t.Fatalf("ndjson line %q: %v", l, err)
					}
					gotLines = append(gotLines, v)
				}
				var wantLines []any
				for _, l := range want.NDJSON {
					var v any
					_ = json.Unmarshal(l, &v)
					wantLines = append(wantLines, v)
				}
				if !reflect.DeepEqual(gotLines, wantLines) {
					t.Fatalf("ndjson %v want %v", gotLines, wantLines)
				}
			case want.Body != nil:
				var g, w any
				if err := json.Unmarshal([]byte(gotBody), &g); err != nil {
					t.Fatalf("request body %q: %v", gotBody, err)
				}
				_ = json.Unmarshal(want.Body, &w)
				if !reflect.DeepEqual(g, w) {
					t.Fatalf("request body %v want %v", g, w)
				}
			default:
				if gotBody != "" {
					t.Fatalf("unexpected request body %q", gotBody)
				}
			}

			if c.Expect.Error != nil {
				var re *RelataError
				if !errors.As(callErr, &re) {
					t.Fatalf("expected a *RelataError, got %v", callErr)
				}
				if re.StatusCode != c.Expect.Error.Status || re.Code != c.Expect.Error.Code {
					t.Fatalf("error %d %q want %d %q", re.StatusCode, re.Code, c.Expect.Error.Status, c.Expect.Error.Code)
				}
				if want := c.Expect.Error.CurrentVersion; want != nil && (re.CurrentVersion == nil || *re.CurrentVersion != *want) {
					t.Fatalf("current_version %v want %d", re.CurrentVersion, *want)
				}
				return
			}
			if callErr != nil {
				t.Fatalf("%s: %v", c.Call.Op, callErr)
			}
			exp := c.Expect.Result
			if o, ok := c.ExpectOverride["go"]; ok {
				exp = o.Result
			}
			if !reflect.DeepEqual(got, exp) {
				t.Fatalf("result %#v want %#v", got, exp)
			}
		})
	}
}
