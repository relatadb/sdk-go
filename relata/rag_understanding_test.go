package relata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for the SDK-side RAG query-understanding layer (#4577, epic #4576)
// — mirrors sdks/python/tests/test_rag_understanding.py's acceptance
// criteria: numeric-intent HyDE skip, conjunction/enumeration shape
// divergence, RRF auto-scaling, each SQL-routable shape, content-safety
// refusal. Uses a real httptest.Server, path-routed, no live server.

func understandingTestHit(chunkID string) RagHit {
	return RagHit{
		BM25Score:   1.0,
		VectorScore: 1.0,
		ChunkID:     chunkID,
		ReportID:    "doc-1",
		Text:        "text for " + chunkID,
		PageStart:   1,
		PageEnd:     1,
	}
}

// routedServer dispatches by URL path, mirroring the Python test suite's
// httpx.MockTransport per-path handlers.
func routedServer(t *testing.T, handlers map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := handlers[r.URL.Path]
		if !ok {
			t.Fatalf("unexpected call to %s", r.URL.Path)
		}
		h(w, r)
	}))
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var out map[string]any
	if len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(body)
	_, _ = w.Write(b)
}

// ── query-shape dispatch — classification ───────────────────────────────────

func TestClassifyQueryShape_Conjunction(t *testing.T) {
	if got := ClassifyQueryShape("Who approved the budget and signed the contract?"); got != QueryShapeConjunction {
		t.Fatalf("got %v, want conjunction", got)
	}
}

func TestClassifyQueryShape_Enumeration(t *testing.T) {
	if got := ClassifyQueryShape("Which vendors were flagged for compliance issues?"); got != QueryShapeEnumeration {
		t.Fatalf("got %v, want enumeration", got)
	}
	if got := ClassifyQueryShape("List every open finding."); got != QueryShapeEnumeration {
		t.Fatalf("got %v, want enumeration", got)
	}
}

func TestClassifyQueryShape_HowManyIsAggregationNotEnumeration(t *testing.T) {
	if got := ClassifyQueryShape("How many incidents were filed last year?"); got != QueryShapeAggregation {
		t.Fatalf("got %v, want aggregation (#4535)", got)
	}
}

func TestClassifyQueryShape_Simple(t *testing.T) {
	if got := ClassifyQueryShape("What is RelataDB?"); got != QueryShapeSimple {
		t.Fatalf("got %v, want simple", got)
	}
}

func TestClassifyQueryShape_AttributeFilterCheckedBeforeEnumeration(t *testing.T) {
	query := "list of persons above 6ft tall with moustache"
	if got := ClassifyQueryShape(query); got != QueryShapeAttributeFilter {
		t.Fatalf("got %v, want attribute_filter", got)
	}
	if !IsAttributeFilterIntent(query) {
		t.Fatal("IsAttributeFilterIntent = false")
	}
}

func TestClassifyQueryShape_NegationCheckedBeforeEnumeration(t *testing.T) {
	if got := ClassifyQueryShape("Which SIMI members are NOT in custody?"); got != QueryShapeNegation {
		t.Fatalf("got %v, want negation", got)
	}
}

func TestIntentDetectors(t *testing.T) {
	if !IsAggregationIntent("What is the total number of open findings?") {
		t.Fatal("IsAggregationIntent = false")
	}
	if IsAggregationIntent("What is RelataDB?") {
		t.Fatal("IsAggregationIntent = true for a plain question")
	}
	if !IsNegationIntent("Which SIMI members are NOT in custody?") {
		t.Fatal("IsNegationIntent = false")
	}
	if !IsBooleanIntent("Members of SIMI AND LeT") {
		t.Fatal("IsBooleanIntent = false")
	}
	if !IsRankingIntent("Top 5 most active members") {
		t.Fatal("IsRankingIntent = false")
	}
	if IsRankingIntent("What is RelataDB?") {
		t.Fatal("IsRankingIntent = true for a plain question")
	}
	if got := ClassifyQueryShape("Tell me about SIMI's history"); got != QueryShapeSimple {
		t.Fatalf("got %v, want simple", got)
	}
}

// ── smart_rag_query — the different /rag/query call shape it produces ──────

func TestSmartRagQuery_ConjunctionRequestsExpandWindow(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	_, err := SmartRagQuery(context.Background(), rag, "Who approved the budget and signed the contract?", "DocumentChunk", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if captured["expand_window"] != true {
		t.Fatalf("expand_window = %v, want true", captured["expand_window"])
	}
}

func TestSmartRagQuery_EnumerationWidensTopK(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	_, err := SmartRagQuery(context.Background(), rag, "Which vendors were flagged for compliance issues?", "DocumentChunk", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if captured["top_k"].(float64) != float64(EnumerationTopK) {
		t.Fatalf("top_k = %v, want %d", captured["top_k"], EnumerationTopK)
	}
}

func TestSmartRagQuery_EnumerationNeverShrinksExplicitLargerTopK(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	topK := 100
	_, err := SmartRagQuery(context.Background(), rag, "Which vendors were flagged?", "DocumentChunk", SmartRagQueryOptions{
		Purpose:    "research",
		RagOptions: SmartRagOptions{TopK: &topK},
	})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if captured["top_k"].(float64) != 100 {
		t.Fatalf("top_k = %v, want 100", captured["top_k"])
	}
}

func TestSmartRagQuery_SimpleShapeLeavesRequestAlone(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	_, err := SmartRagQuery(context.Background(), rag, "What is RelataDB?", "DocumentChunk", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if captured["top_k"].(float64) != float64(RagDefaultTopK) {
		t.Fatalf("top_k = %v, want %d", captured["top_k"], RagDefaultTopK)
	}
	// RagQueryRequest.ExpandWindow carries `omitempty` (rag.go) — a
	// caller-default `false` is omitted from the wire body entirely, same
	// as an explicit false would render identically server-side.
	if v, present := captured["expand_window"]; present && v != false {
		t.Fatalf("expand_window = %v, want false or absent", v)
	}
}

// ── HyDE + the numeric-intent guard ──────────────────────────────────────────

func TestIsNumericIntent(t *testing.T) {
	for _, q := range []string{
		"What was the total revenue for Q1?",
		"What percentage of patients responded to treatment?",
		"How many incidents were filed last year?",
		"What is the recommended dosage?",
	} {
		if !IsNumericIntent(q) {
			t.Fatalf("IsNumericIntent(%q) = false, want true", q)
		}
	}
	if IsNumericIntent("Explain how RelataDB performs hybrid retrieval.") {
		t.Fatal("IsNumericIntent = true for a non-numeric query")
	}
}

func TestExpandQueryHyde_SkippedForNumericIntent(t *testing.T) {
	var calls []string
	query := "What was the total revenue for Q1?"
	result, err := ExpandQueryHyde(context.Background(), query, func(_ context.Context, q string) (string, error) {
		calls = append(calls, q)
		return "a hallucinated number", nil
	})
	if err != nil {
		t.Fatalf("ExpandQueryHyde: %v", err)
	}
	if result != query {
		t.Fatalf("result = %q, want unchanged query", result)
	}
	if len(calls) != 0 {
		t.Fatalf("hypothesisFn called %d times, want 0", len(calls))
	}
}

func TestExpandQueryHyde_AppliedForNonNumericQuery(t *testing.T) {
	query := "How does hybrid retrieval work?"
	result, err := ExpandQueryHyde(context.Background(), query, func(_ context.Context, q string) (string, error) {
		return fmt.Sprintf("Hypothetically, %s is answered by RelataDB's hybrid engine.", q), nil
	})
	if err != nil {
		t.Fatalf("ExpandQueryHyde: %v", err)
	}
	if result == query {
		t.Fatal("result unchanged, want HyDE expansion")
	}
}

func TestSmartRagQuery_NumericIntentSkipsHyde(t *testing.T) {
	var captured map[string]any
	var hydeCalls []string
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	query := "What percentage of revenue came from repeat customers?"
	_, err := SmartRagQuery(context.Background(), rag, query, "DocumentChunk", SmartRagQueryOptions{
		Purpose: "research",
		HypothesisFn: func(_ context.Context, q string) (string, error) {
			hydeCalls = append(hydeCalls, q)
			return "invented number", nil
		},
	})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if len(hydeCalls) != 0 {
		t.Fatalf("HyDE called %d times, want 0", len(hydeCalls))
	}
	if captured["query"] != query {
		t.Fatalf("query = %v, want unchanged %q", captured["query"], query)
	}
}

// ── decomposition + auto-scaling RRF merge ──────────────────────────────────

func TestDecomposeQuery_SplitsMultiPartQuestion(t *testing.T) {
	parts := DecomposeQuery("What is the incident response policy and who owns it?")
	if len(parts) != 2 {
		t.Fatalf("len(parts) = %d, want 2: %v", len(parts), parts)
	}
	if parts[0] != "What is the incident response policy" {
		t.Fatalf("parts[0] = %q", parts[0])
	}
	if parts[1] != "who owns it?" {
		t.Fatalf("parts[1] = %q", parts[1])
	}
}

func TestDecomposeQuery_SinglePartUnchanged(t *testing.T) {
	parts := DecomposeQuery("What is RelataDB?")
	if len(parts) != 1 || parts[0] != "What is RelataDB?" {
		t.Fatalf("parts = %v", parts)
	}
}

func TestRrfKForFanout_MatchesAutoScalingFormula(t *testing.T) {
	table := []struct {
		n        int
		expected float64
	}{
		{1, 60.0}, {2, 30.0}, {3, 20.0}, {6, 10.0}, {10, 10.0}, {100, 10.0},
	}
	for _, tc := range table {
		got, err := RrfKForFanout(tc.n)
		if err != nil {
			t.Fatalf("RrfKForFanout(%d): %v", tc.n, err)
		}
		if got != tc.expected {
			t.Fatalf("RrfKForFanout(%d) = %v, want %v", tc.n, got, tc.expected)
		}
	}
}

func TestRrfKForFanout_RejectsNonPositiveN(t *testing.T) {
	if _, err := RrfKForFanout(0); err == nil {
		t.Fatal("want error for n=0")
	}
}

func TestRrfScores_UsesGivenK(t *testing.T) {
	respA := &RagQueryResponse{Hits: []RagHit{understandingTestHit("c1"), understandingTestHit("c2")}}
	respB := &RagQueryResponse{Hits: []RagHit{understandingTestHit("c2"), understandingTestHit("c3")}}

	k30 := RrfScores([]*RagQueryResponse{respA, respB}, 30.0)
	if diff := k30["c1"] - 1.0/31.0; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("c1 score = %v", k30["c1"])
	}
	if diff := k30["c2"] - (1.0/32.0 + 1.0/31.0); diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("c2 score = %v", k30["c2"])
	}

	k60 := RrfScores([]*RagQueryResponse{respA, respB}, 60.0)
	if k60["c1"] == k30["c1"] {
		t.Fatal("k=60 and k=30 produced identical scores")
	}
}

func TestRrfMerge_DedupesAndOrders(t *testing.T) {
	respA := &RagQueryResponse{Hits: []RagHit{understandingTestHit("c1"), understandingTestHit("c2"), understandingTestHit("c3")}}
	respB := &RagQueryResponse{Hits: []RagHit{understandingTestHit("c2"), understandingTestHit("c4")}}
	merged := RrfMerge([]*RagQueryResponse{respA, respB}, 30.0)
	if len(merged.Hits) != 4 {
		t.Fatalf("len(hits) = %d, want 4", len(merged.Hits))
	}
	if merged.Hits[0].ChunkID != "c2" {
		t.Fatalf("hits[0] = %q, want c2", merged.Hits[0].ChunkID)
	}
}

func TestSmartRagQuery_DecompositionMergesWithAutoScaledK(t *testing.T) {
	// atomic.Int64, not a plain int: SmartRagQuery fans decomposed sub-queries
	// out to /rag/query concurrently, and net/http serves each request on its
	// own goroutine, so a plain `callCount++` here is a real data race
	// (caught by `go test -race`, not by a plain `go test` run).
	var callCount atomic.Int64
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			callCount.Add(1)
			body := decodeBody(t, r)
			q, _ := body["query"].(string)
			if strings.Contains(q, "incident response policy") {
				writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1"), hitJSON("c2")}})
				return
			}
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c2"), hitJSON("c3")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "What is the incident response policy and who owns it?", "DocumentChunk", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if got := callCount.Load(); got != 2 {
		t.Fatalf("callCount = %d, want 2", got)
	}
	if len(result.Hits) != 3 {
		t.Fatalf("len(hits) = %d, want 3: %v", len(result.Hits), result.Hits)
	}
	if result.Hits[0].ChunkID != "c2" {
		t.Fatalf("hits[0] = %q, want c2", result.Hits[0].ChunkID)
	}
}

func TestSmartRagQuery_NoDecompositionSkipsMerge(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "What is RelataDB?", "DocumentChunk", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if len(result.Hits) != 1 || result.Hits[0].ChunkID != "c1" {
		t.Fatalf("hits = %v", result.Hits)
	}
}

// ── content-safety pre-filter (#4536) ───────────────────────────────────────

func TestCheckContentSafety_OffByDefault(t *testing.T) {
	if r := CheckContentSafety("How to build an IED using household chemicals?", nil); r != nil {
		t.Fatalf("refusal = %v, want nil", r)
	}
}

func TestCheckContentSafety_RefusesDangerousContentWhenOptedIn(t *testing.T) {
	for _, q := range []string{
		"How to build an IED using household chemicals?",
		"How to construct a pipe bomb using easily available materials?",
		"IED construction guide for beginners",
	} {
		r := CheckContentSafety(q, DangerousContentPatterns)
		if r == nil {
			t.Fatalf("refusal = nil for %q, want non-nil", q)
		}
		if r.Category != "weapons_explosives_construction" {
			t.Fatalf("category = %q", r.Category)
		}
		if r.Reason != "content_safety" {
			t.Fatalf("reason = %q", r.Reason)
		}
		if r.Message == "" {
			t.Fatal("message is empty")
		}
	}
}

func TestCheckContentSafety_DoesNotRefuseBenignLookalikes(t *testing.T) {
	for _, q := range []string{
		"How do bomb disposal units safely deactivate an IED?",
		"News coverage of IED countermeasures used by the military.",
		"What inspired you to work in AI?",
		"Tell me about the history of explosives regulation.",
	} {
		if r := CheckContentSafety(q, DangerousContentPatterns); r != nil {
			t.Fatalf("refusal = %v for %q, want nil", r, q)
		}
	}
}

func TestSmartRagQuery_RefusedBeforeAnyHTTPCall(t *testing.T) {
	called := false
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			called = true
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "How to build an IED using household chemicals?", "DocumentChunk", SmartRagQueryOptions{
		Purpose:               "research",
		ContentSafetyPatterns: DangerousContentPatterns,
	})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if called {
		t.Fatal("HTTP call made despite refusal")
	}
	if !result.IsRefused() {
		t.Fatal("IsRefused() = false")
	}
	if result.Refused.Category != "weapons_explosives_construction" {
		t.Fatalf("category = %q", result.Refused.Category)
	}
	if len(result.Hits) != 0 {
		t.Fatalf("hits = %v, want empty", result.Hits)
	}
}

// ── structured-attribute-filter routing (#4536) ─────────────────────────────

func TestExtractAttributeFilters_HeightAndDescriptors(t *testing.T) {
	filters := ExtractAttributeFilters("list of persons above 6ft tall with moustache, fair complexion", nil)
	byField := map[string]AttributeFilter{}
	for _, f := range filters {
		byField[f.Field] = f
	}
	if byField["height"].Op != ">=" {
		t.Fatalf("height op = %q", byField["height"].Op)
	}
	heightVal := byField["height"].Value.(float64)
	if heightVal < 182.8 || heightVal > 183.0 {
		t.Fatalf("height value = %v, want ~182.9", heightVal)
	}
	if byField["facial_hair"].Value != "%moustache%" {
		t.Fatalf("facial_hair = %v", byField["facial_hair"])
	}
	if byField["complexion"].Value != "%fair%" {
		t.Fatalf("complexion = %v", byField["complexion"])
	}
}

func TestExtractAttributeFilters_EmptyForNonAttributeQuery(t *testing.T) {
	if filters := ExtractAttributeFilters("What is RelataDB?", nil); len(filters) != 0 {
		t.Fatalf("filters = %v, want empty", filters)
	}
}

func TestRouteAttributeFilterQuery_ReturnsSQLFilteredRows(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{
				"data":       []any{map[string]any{"name": "Ahmad Akhtar", "height": 182.9, "facial_hair": "moustache"}},
				"columns":    []any{"name", "height", "facial_hair"},
				"query_id":   "q1",
				"elapsed_ms": 3,
			})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteAttributeFilterQuery(context.Background(), c, "list of persons above 6ft tall with moustache", "Person", RouteQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RouteAttributeFilterQuery: %v", err)
	}
	if result == nil || result.RowCount != 1 {
		t.Fatalf("result = %v", result)
	}
	if result.Rows[0]["name"] != "Ahmad Akhtar" {
		t.Fatalf("rows[0] = %v", result.Rows[0])
	}
	sql, _ := captured["sql"].(string)
	if !strings.Contains(sql, "Person") || !strings.Contains(sql, "height >=") || !strings.Contains(sql, "facial_hair ILIKE") {
		t.Fatalf("sql = %q", sql)
	}
}

func TestRouteAttributeFilterQuery_NilWhenNoFiltersExtracted(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteAttributeFilterQuery(context.Background(), c, "What is RelataDB?", "Person", RouteQueryOptions{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != nil {
		t.Fatalf("result = %v, want nil", result)
	}
}

func TestRouteAttributeFilterQuery_FallsBackWhenKnownFieldsDontMatch(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteAttributeFilterQuery(context.Background(), c, "list of persons above 6ft tall with moustache", "Person", RouteQueryOptions{
		KnownFields: []string{"name", "email"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != nil {
		t.Fatalf("result = %v, want nil", result)
	}
}

func TestSmartRagQuery_RoutesAttributeFilterToSQLNotRetrieval(t *testing.T) {
	ragCalled := false
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			ragCalled = true
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
		"/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{
				"data":     []any{map[string]any{"name": "Ahmad Akhtar", "height": 182.9}},
				"columns":  []any{"name", "height"},
				"query_id": "q1",
			})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "list of persons above 6ft tall with moustache", "Person", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if ragCalled {
		t.Fatal("/rag/query called for an attribute-filter shape")
	}
	if !result.IsSQLRouted() {
		t.Fatal("IsSQLRouted() = false")
	}
	if result.SQLResult.Rows[0]["name"] != "Ahmad Akhtar" {
		t.Fatalf("rows[0] = %v", result.SQLResult.Rows[0])
	}
	if len(result.Hits) != 0 {
		t.Fatalf("hits = %v, want empty", result.Hits)
	}
}

func TestSmartRagQuery_AttributeFilterFallsBackWithLowConfidence(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "list of persons above 6ft tall with moustache", "Person", SmartRagQueryOptions{
		Purpose:              "research",
		AttributeKnownFields: []string{"name", "email"},
	})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if result.IsSQLRouted() {
		t.Fatal("IsSQLRouted() = true, want false")
	}
	if !result.LowConfidence {
		t.Fatal("LowConfidence = false")
	}
	if result.LowConfidenceReason == "" {
		t.Fatal("LowConfidenceReason is empty")
	}
	if len(result.Hits) != 1 || result.Hits[0].ChunkID != "c1" {
		t.Fatalf("hits = %v", result.Hits)
	}
}

// ── aggregation/negation/boolean/ranking SQL routing (#4535) ───────────────

func TestExtractKeywordFilters_BuildsPredicatesFromFieldMap(t *testing.T) {
	filters := ExtractKeywordFilters("Which SIMI members are NOT in custody?", map[string]string{"simi": "organization", "custody": "status"}, "NOT ILIKE", true)
	byField := map[string]AttributeFilter{}
	for _, f := range filters {
		byField[f.Field] = f
	}
	if byField["organization"].Value != "%simi%" || byField["organization"].Op != "NOT ILIKE" {
		t.Fatalf("organization = %v", byField["organization"])
	}
	if byField["status"].Value != "%custody%" {
		t.Fatalf("status = %v", byField["status"])
	}
}

func TestExtractKeywordFilters_DedupeFieldsOffKeepsBothMatches(t *testing.T) {
	filters := ExtractKeywordFilters("Members of SIMI AND LeT", map[string]string{"simi": "organization", "let": "organization"}, "=", false)
	if len(filters) != 2 {
		t.Fatalf("len(filters) = %d, want 2", len(filters))
	}
	for _, f := range filters {
		if f.Field != "organization" {
			t.Fatalf("field = %q, want organization", f.Field)
		}
	}
}

func TestRouteAggregationQuery_BareCountNeedsNoFieldMap(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"count": 42}}, "columns": []any{"count"}, "query_id": "q1"})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteAggregationQuery(context.Background(), c, "How many incidents happened in 2023?", "Incident", RouteQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Rows[0]["count"] != float64(42) {
		t.Fatalf("count = %v", result.Rows[0]["count"])
	}
	if captured["sql"] != "SELECT COUNT(*) AS count FROM Incident" {
		t.Fatalf("sql = %v", captured["sql"])
	}
}

func TestRouteAggregationQuery_FallsBackWhenKnownFieldsDontMatch(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteAggregationQuery(context.Background(), c, "How many members are from SIMI?", "Person", RouteQueryOptions{
		FieldMap:    map[string]string{"simi": "organization"},
		KnownFields: []string{"name"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != nil {
		t.Fatalf("result = %v, want nil", result)
	}
}

func TestRouteNegationQuery_NilWithoutFieldMap(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteNegationQuery(context.Background(), c, "Which SIMI members are NOT in custody?", "Person", RouteQueryOptions{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != nil {
		t.Fatalf("result = %v, want nil", result)
	}
}

func TestRouteNegationQuery_BuildsNotIlikePredicate(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"name": "Bilal Hassan", "status": "at_large"}}, "columns": []any{"name", "status"}, "query_id": "q1"})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteNegationQuery(context.Background(), c, "Which SIMI members are NOT in custody?", "Person", RouteQueryOptions{
		FieldMap: map[string]string{"custody": "status"},
		Purpose:  "research",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Rows[0]["name"] != "Bilal Hassan" {
		t.Fatalf("rows[0] = %v", result.Rows[0])
	}
	sql, _ := captured["sql"].(string)
	if !strings.Contains(sql, "status NOT ILIKE") {
		t.Fatalf("sql = %q", sql)
	}
}

func TestRouteBooleanQuery_JoinsPredicatesWithAndOr(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"data": []any{}, "columns": []any{}, "query_id": "q1"})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	_, err := RouteBooleanQuery(context.Background(), c, "Members of SIMI AND LeT", "Person", RouteQueryOptions{
		FieldMap: map[string]string{"simi": "organization", "let": "organization"},
		Purpose:  "research",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	sql, _ := captured["sql"].(string)
	if !strings.Contains(sql, "organization = 'simi'") || !strings.Contains(sql, "organization = 'let'") || !strings.Contains(sql, " AND ") || strings.Contains(sql, " OR ") {
		t.Fatalf("sql = %q", sql)
	}

	_, err = RouteBooleanQuery(context.Background(), c, "Members of SIMI or LeT", "Person", RouteQueryOptions{
		FieldMap: map[string]string{"simi": "organization", "let": "organization"},
		Purpose:  "research",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	sql, _ = captured["sql"].(string)
	if !strings.Contains(sql, " OR ") {
		t.Fatalf("sql = %q, want OR", sql)
	}
}

func TestRouteBooleanQuery_NilWhenFewerThanTwoPredicatesResolve(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteBooleanQuery(context.Background(), c, "Members of SIMI AND LeT", "Person", RouteQueryOptions{
		FieldMap: map[string]string{"simi": "organization"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != nil {
		t.Fatalf("result = %v, want nil", result)
	}

	result2, err := RouteBooleanQuery(context.Background(), c, "Members of SIMI AND LeT", "Person", RouteQueryOptions{
		FieldMap:    map[string]string{"simi": "organization", "let": "organization"},
		KnownFields: []string{"name"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result2 != nil {
		t.Fatalf("result2 = %v, want nil", result2)
	}
}

func TestRouteRankingQuery_BuildsOrderByLimit(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"name": "Zahid Iqbal", "activity_count": 91}}, "columns": []any{"name", "activity_count"}, "query_id": "q1"})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteRankingQuery(context.Background(), c, "Top 5 most active members", "Person", RouteQueryOptions{
		FieldMap: map[string]string{"active": "activity_count"},
		Purpose:  "research",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Rows[0]["name"] != "Zahid Iqbal" {
		t.Fatalf("rows[0] = %v", result.Rows[0])
	}
	if captured["sql"] != "SELECT * FROM Person ORDER BY activity_count DESC LIMIT 5" {
		t.Fatalf("sql = %v", captured["sql"])
	}
}

func TestRouteRankingQuery_DefaultsLimitAndGoesAscendingForLowest(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/query": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 200, map[string]any{"data": []any{}, "columns": []any{}, "query_id": "q1"})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	_, err := RouteRankingQuery(context.Background(), c, "Who is the least active member?", "Person", RouteQueryOptions{
		FieldMap: map[string]string{"active": "activity_count"},
		Purpose:  "research",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	sql, _ := captured["sql"].(string)
	if !strings.Contains(sql, "ORDER BY activity_count ASC") || !strings.Contains(sql, fmt.Sprintf("LIMIT %d", DefaultRankingLimit)) {
		t.Fatalf("sql = %q", sql)
	}
}

func TestRouteRankingQuery_FallsBackWhenKnownFieldsDontMatch(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteRankingQuery(context.Background(), c, "Top 5 most active members", "Person", RouteQueryOptions{
		FieldMap:    map[string]string{"active": "activity_count"},
		KnownFields: []string{"name"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != nil {
		t.Fatalf("result = %v, want nil", result)
	}
}

// ── enumeration (#4535 large-result-set policy) ─────────────────────────────

func TestRouteEnumerationQuery_ReturnsInlineResultWhenSmall(t *testing.T) {
	var captured map[string]any
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/export": func(w http.ResponseWriter, r *http.Request) {
			captured = decodeBody(t, r)
			writeJSON(w, 202, map[string]any{"operation_id": "op-1", "status": "running"})
		},
		"/v1/operations/op-1": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"row_count": 3, "columns": []any{"a"}, "data": []any{
				map[string]any{"a": float64(1)}, map[string]any{"a": float64(2)}, map[string]any{"a": float64(3)},
			}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteEnumerationQuery(context.Background(), c, "Give me all incidents", "Incident", RouteEnumerationOptions{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if captured["sql"] != "SELECT * FROM Incident" {
		t.Fatalf("sql = %v", captured["sql"])
	}
	if result.RowCount != 3 {
		t.Fatalf("row_count = %d", result.RowCount)
	}
	if result.IsFileBacked() {
		t.Fatal("IsFileBacked() = true, want false")
	}
	if len(result.Data) != 3 {
		t.Fatalf("data = %v", result.Data)
	}
	if result.Bucket != "" {
		t.Fatalf("bucket = %q, want empty", result.Bucket)
	}
}

func TestRouteEnumerationQuery_IsFileBackedWhenLarge(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/export": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 202, map[string]any{"operation_id": "op-2", "status": "running"})
		},
		"/v1/operations/op-2": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{
				"row_count":    50000,
				"columns":      []any{"msisdn", "called_at"},
				"preview":      []any{map[string]any{"msisdn": "9800004040", "called_at": "2023-01-01"}},
				"preview_note": "preview of the first 1 of 50000 row(s)",
				"bucket":       "default",
				"key":          "CallRecord-9800004040-2023-01-01_to_2023-12-31-1700000000.csv",
				"etag":         "abc123",
				"content_type": "text/csv",
				"size_bytes":   4500000,
			})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteEnumerationQuery(context.Background(), c, "Give me all calls made by 9800004040 last year", "CallRecord", RouteEnumerationOptions{
		KeyFilter: "9800004040", DateFrom: "2023-01-01", DateTo: "2023-12-31",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.RowCount != 50000 {
		t.Fatalf("row_count = %d", result.RowCount)
	}
	if !result.IsFileBacked() {
		t.Fatal("IsFileBacked() = false, want true")
	}
	if result.Bucket != "default" {
		t.Fatalf("bucket = %q", result.Bucket)
	}
	if result.ETag != "abc123" {
		t.Fatalf("etag = %q", result.ETag)
	}
	if result.Data != nil {
		t.Fatalf("data = %v, want nil", result.Data)
	}
}

func TestRouteEnumerationQuery_FallsBackWhenKnownFieldsDontMatch(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	result, err := RouteEnumerationQuery(context.Background(), c, "Give me all SIMI members", "Person", RouteEnumerationOptions{
		FieldMap:    map[string]string{"simi": "organization"},
		KnownFields: []string{"name"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != nil {
		t.Fatalf("result = %v, want nil", result)
	}
}

func TestRouteEnumerationQuery_TimesOutWhenNeverCompletes(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/export": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 202, map[string]any{"operation_id": "op-3", "status": "running"})
		},
		"/v1/operations/op-3": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"status": "running"})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	_, err := RouteEnumerationQuery(context.Background(), c, "Give me all X", "T", RouteEnumerationOptions{
		PollTimeout:  20 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if !errors.Is(err, ErrEnumerationTimeout) {
		t.Fatalf("err = %v, want ErrEnumerationTimeout", err)
	}
}

// ── smart_rag_query end-to-end dispatch ──────────────────────────────────────

func TestSmartRagQuery_RoutesAggregationToCountNotEnumeration(t *testing.T) {
	ragCalled := false
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			ragCalled = true
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
		"/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"count": 1234}}, "columns": []any{"count"}, "query_id": "q1"})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "How many incidents happened in 2023?", "Incident", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if ragCalled {
		t.Fatal("/rag/query called for an aggregation shape")
	}
	if !result.IsSQLRouted() {
		t.Fatal("IsSQLRouted() = false")
	}
	if result.SQLResult.RowCount != 1 {
		t.Fatalf("row_count = %d, want 1", result.SQLResult.RowCount)
	}
	if result.SQLResult.Rows[0]["count"] != float64(1234) {
		t.Fatalf("count = %v", result.SQLResult.Rows[0]["count"])
	}
}

func TestSmartRagQuery_NonStructuredQuestionRoutesToRetrieval(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "Tell me about SIMI's history", "DocumentChunk", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if result.IsSQLRouted() {
		t.Fatal("IsSQLRouted() = true, want false")
	}
	if len(result.Hits) != 1 || result.Hits[0].ChunkID != "c1" {
		t.Fatalf("hits = %v", result.Hits)
	}
}

func TestSmartRagQuery_RoutesBooleanToSQL(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"name": "Bilal Hassan", "organization": "SIMI"}}, "columns": []any{"name", "organization"}, "query_id": "q1"})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "Members of SIMI AND LeT", "Person", SmartRagQueryOptions{
		Purpose:            "research",
		StructuredFieldMap: map[string]string{"simi": "organization", "let": "organization"},
	})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if !result.IsSQLRouted() {
		t.Fatal("IsSQLRouted() = false")
	}
	if result.SQLResult.Rows[0]["name"] != "Bilal Hassan" {
		t.Fatalf("rows[0] = %v", result.SQLResult.Rows[0])
	}
}

func TestSmartRagQuery_NegationFallsBackWithLowConfidence(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)
	result, err := SmartRagQuery(context.Background(), rag, "Which SIMI members are NOT in custody?", "Person", SmartRagQueryOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("SmartRagQuery: %v", err)
	}
	if result.IsSQLRouted() {
		t.Fatal("IsSQLRouted() = true, want false")
	}
	if !result.LowConfidence {
		t.Fatal("LowConfidence = false")
	}
	if len(result.Hits) != 1 || result.Hits[0].ChunkID != "c1" {
		t.Fatalf("hits = %v", result.Hits)
	}
}

// ── small helpers ────────────────────────────────────────────────────────────

func hitJSON(chunkID string) map[string]any {
	return map[string]any{
		"bm25_score":   1.0,
		"vector_score": 1.0,
		"chunk_id":     chunkID,
		"report_id":    "doc-1",
		"text":         "text for " + chunkID,
		"page_start":   1,
		"page_end":     1,
	}
}
