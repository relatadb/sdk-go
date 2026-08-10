package relata

import (
	"context"
	"math"
	"net/http"
	"sync/atomic"
	"testing"
)

// Tests for the heuristic gate + corrective retrieval grading + loop
// confidence cost ladder (#4525), plus sub-agent fan-out + deterministic
// merge (#4526) — #4582 (epic #4576).
//
// Mirrors sdks/python/tests/test_rag_loop.py and
// sdks/python/tests/test_rag_fanout.py's acceptance criteria: the
// zero-LLM-calls-on-heuristic-pass property, MaxIterations enforcement
// under adversarial input, deterministic fan-out winner selection, and the
// LowConfidenceFloor/MergeThreshold merge behavior. Reuses
// routedServer/decodeBody/writeJSON/newTestClient/understandingTestHit from
// rag_understanding_test.go (same package, same-file-family test helpers).

// embeddingFnForScore returns an embedding function under which any query
// text's embedding vs. any hit text's embedding (hit text always starts
// with "text for ", per understandingTestHit above) has cosine similarity
// exactly score.
func embeddingFnForScore(score float64) EmbeddingFn {
	queryVec := []float64{1.0, 0.0}
	orthogonal := math.Sqrt(math.Max(0, 1-score*score))
	hitVec := []float64{score, orthogonal}
	return func(_ context.Context, text string) ([]float64, error) {
		if len(text) >= len("text for ") && text[:len("text for ")] == "text for " {
			return hitVec, nil
		}
		return queryVec, nil
	}
}

func fanoutHitJSON(chunkID string, confidence *float64) map[string]any {
	m := hitJSON(chunkID)
	if confidence != nil {
		m["relevance_confidence"] = *confidence
	}
	return m
}

func f64(v float64) *float64 { return &v }

// ── heuristic gate — threshold defaults, verified against the ticket ───────

func TestRagLoop_ThresholdDefaultsMatchVerifiedValues(t *testing.T) {
	if HeuristicPassThreshold != 0.85 || HeuristicRetryThreshold != 0.60 ||
		LoopConfidenceThreshold != 0.85 || MaxIterations != 5 {
		t.Fatalf("threshold defaults drifted: pass=%v retry=%v loop=%v max=%v",
			HeuristicPassThreshold, HeuristicRetryThreshold, LoopConfidenceThreshold, MaxIterations)
	}
}

func TestHeuristicGate_PassesAboveThreshold(t *testing.T) {
	result := HeuristicGate([]float64{1.0, 0.0}, [][]float64{{1.0, 0.0}, {1.0, 0.0}})
	if math.Abs(result.Score-1.0) > 1e-9 || result.Decision != GateDecisionPass {
		t.Fatalf("got %+v, want score~1.0 decision=pass", result)
	}
}

func TestHeuristicGate_RetriesBelowThreshold(t *testing.T) {
	result := HeuristicGate([]float64{1.0, 0.0}, [][]float64{{0.0, 1.0}})
	if math.Abs(result.Score-0.0) > 1e-9 || result.Decision != GateDecisionRetry {
		t.Fatalf("got %+v, want score~0.0 decision=retry", result)
	}
}

func TestHeuristicGate_EvaluatesBetweenThresholds(t *testing.T) {
	result := HeuristicGate([]float64{1.0, 0.0}, [][]float64{{0.7, math.Sqrt(1 - 0.7*0.7)}})
	if math.Abs(result.Score-0.7) > 1e-9 || result.Decision != GateDecisionEvaluate {
		t.Fatalf("got %+v, want score~0.7 decision=evaluate", result)
	}
}

func TestHeuristicGate_NoHitsRetries(t *testing.T) {
	result := HeuristicGate([]float64{1.0, 0.0}, nil)
	if result.Score != 0.0 || result.Decision != GateDecisionRetry {
		t.Fatalf("got %+v, want score=0.0 decision=retry", result)
	}
}

func TestHeuristicGate_BoundaryValuesFallIntoEvaluate(t *testing.T) {
	// Strict inequalities: exactly at either bar is neither an auto-pass
	// nor an auto-retry — it must go to the (cheap-model) evaluator, not be
	// silently treated as confident or worthless.
	//
	// The boundary components are built from `float64` variables, not
	// inline untyped-constant expressions (e.g. NOT `math.Sqrt(1 -
	// 0.85*0.85)` written directly in the call) — Go evaluates untyped
	// constant expressions at compile time with >=256-bit precision and
	// rounds to float64 only once at the end, which can round a knife-edge
	// case like this one to the opposite side of a runtime,
	// standard-IEEE754-double-rounded computation (the semantics Python's
	// `math.sqrt(1 - 0.85**2)` and this package's runtime arithmetic both
	// use). Routing through named variables forces the same
	// runtime-float64, round-at-every-step evaluation as the library code
	// under test.
	passBar := 0.85
	passHitY := math.Sqrt(1 - passBar*passBar)
	passBoundary := HeuristicGate([]float64{1.0, 0.0}, [][]float64{{passBar, passHitY}})
	if passBoundary.Decision != GateDecisionEvaluate {
		t.Fatalf("pass boundary decision = %v (score %v), want evaluate", passBoundary.Decision, passBoundary.Score)
	}
	retryBar := 0.60
	retryHitY := math.Sqrt(1 - retryBar*retryBar)
	retryBoundary := HeuristicGate([]float64{1.0, 0.0}, [][]float64{{retryBar, retryHitY}})
	if retryBoundary.Decision != GateDecisionEvaluate {
		t.Fatalf("retry boundary decision = %v (score %v), want evaluate", retryBoundary.Decision, retryBoundary.Score)
	}
}

// ── corrective retrieval grading ────────────────────────────────────────────

func TestGradeHits_ComputesFractionCorrect(t *testing.T) {
	hits := []RagHit{understandingTestHit("c1"), understandingTestHit("c2"), understandingTestHit("c3")}
	graderFn := func(_ context.Context, _ string, _ []RagHit) ([]HitGrade, error) {
		return []HitGrade{HitGradeCorrect, HitGradeIncorrect, HitGradeAmbiguous}, nil
	}
	result, err := GradeHits(context.Background(), "q", hits, graderFn)
	if err != nil {
		t.Fatalf("GradeHits: %v", err)
	}
	if math.Abs(result.FractionCorrect-1.0/3.0) > 1e-9 {
		t.Fatalf("fraction correct = %v, want 1/3", result.FractionCorrect)
	}
	want := []HitGrade{HitGradeCorrect, HitGradeIncorrect, HitGradeAmbiguous}
	for i, g := range want {
		if result.Grades[i] != g {
			t.Fatalf("grades[%d] = %v, want %v", i, result.Grades[i], g)
		}
	}
}

func TestGradeHits_NeedsRequeryBelowFloor(t *testing.T) {
	hits := []RagHit{understandingTestHit("c1"), understandingTestHit("c2")}
	graderFn := func(_ context.Context, _ string, _ []RagHit) ([]HitGrade, error) {
		return []HitGrade{HitGradeIncorrect, HitGradeIncorrect}, nil
	}
	result, err := GradeHits(context.Background(), "q", hits, graderFn)
	if err != nil {
		t.Fatalf("GradeHits: %v", err)
	}
	if result.FractionCorrect >= CorrectiveFractionCorrectFloor || !result.NeedsRequery() {
		t.Fatalf("got %+v, want below floor + needs requery", result)
	}
}

func TestGradeHits_DoesNotNeedRequeryAboveFloor(t *testing.T) {
	hits := []RagHit{understandingTestHit("c1"), understandingTestHit("c2")}
	graderFn := func(_ context.Context, _ string, _ []RagHit) ([]HitGrade, error) {
		return []HitGrade{HitGradeCorrect, HitGradeCorrect}, nil
	}
	result, err := GradeHits(context.Background(), "q", hits, graderFn)
	if err != nil {
		t.Fatalf("GradeHits: %v", err)
	}
	if result.NeedsRequery() {
		t.Fatal("NeedsRequery() = true, want false")
	}
}

func TestGradeHits_EmptyHitsShortCircuits(t *testing.T) {
	result, err := GradeHits(context.Background(), "q", nil, func(context.Context, string, []RagHit) ([]HitGrade, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("GradeHits: %v", err)
	}
	if result.FractionCorrect != 0.0 || len(result.Grades) != 0 {
		t.Fatalf("got %+v, want zero-value empty result", result)
	}
}

func TestGradeHits_RejectsMismatchedGradeCount(t *testing.T) {
	hits := []RagHit{understandingTestHit("c1"), understandingTestHit("c2")}
	_, err := GradeHits(context.Background(), "q", hits, func(context.Context, string, []RagHit) ([]HitGrade, error) {
		return []HitGrade{HitGradeCorrect}, nil
	})
	if err == nil {
		t.Fatal("GradeHits: want error on mismatched grade count, got nil")
	}
}

// ── RunAgenticLoop — the zero-LLM-calls property (AC #2) ───────────────────

func TestRunAgenticLoop_Iteration0HeuristicPassMakesZeroLLMCalls(t *testing.T) {
	var queryCallCount int32
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&queryCallCount, 1)
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1"), hitJSON("c2")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	var graderCalls, hydeCalls int32
	graderFn := func(_ context.Context, _ string, hits []RagHit) ([]HitGrade, error) {
		atomic.AddInt32(&graderCalls, 1)
		grades := make([]HitGrade, len(hits))
		for i := range grades {
			grades[i] = HitGradeCorrect
		}
		return grades, nil
	}
	hypothesisFn := func(_ context.Context, _ string) (string, error) {
		atomic.AddInt32(&hydeCalls, 1)
		return "hypothetical answer", nil
	}

	result, err := RunAgenticLoop(context.Background(), rag, "What is RelataDB?", "DocumentChunk", RunAgenticLoopOptions{
		Purpose:      "research",
		EmbeddingFn:  embeddingFnForScore(1.0), // clears the pass bar
		GraderFn:     graderFn,
		HypothesisFn: hypothesisFn,
	})
	if err != nil {
		t.Fatalf("RunAgenticLoop: %v", err)
	}

	if result.StoppedReason != "heuristic_pass" {
		t.Fatalf("stopped reason = %q, want heuristic_pass", result.StoppedReason)
	}
	if result.LLMCalls != 0 {
		t.Fatalf("llm calls = %d, want 0", result.LLMCalls)
	}
	if atomic.LoadInt32(&queryCallCount) != 1 {
		t.Fatalf("query call count = %d, want 1", queryCallCount)
	}
	if atomic.LoadInt32(&graderCalls) != 0 || atomic.LoadInt32(&hydeCalls) != 0 {
		t.Fatal("grader/hyde must never be invoked on a heuristic pass")
	}
	if len(result.Iterations) != 1 {
		t.Fatalf("iterations = %d, want 1", len(result.Iterations))
	}
	if result.Iterations[0].Confidence == nil || math.Abs(*result.Iterations[0].Confidence-1.0) > 1e-9 {
		t.Fatalf("iteration confidence = %v, want ~1.0", result.Iterations[0].Confidence)
	}
}

// ── RunAgenticLoop — MaxIterations hard cap under adversarial input ────────

func TestRunAgenticLoop_HeuristicRetryIsBoundedByMaxIterations(t *testing.T) {
	var queryCallCount int32
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&queryCallCount, 1)
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	// Score 0.0 never clears the retry floor — an adversarial EmbeddingFn
	// that always forces GateDecisionRetry.
	result, err := RunAgenticLoop(context.Background(), rag, "adversarial query", "DocumentChunk", RunAgenticLoopOptions{
		Purpose:       "research",
		EmbeddingFn:   embeddingFnForScore(0.0),
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("RunAgenticLoop: %v", err)
	}
	if result.StoppedReason != "max_iterations" {
		t.Fatalf("stopped reason = %q, want max_iterations", result.StoppedReason)
	}
	if atomic.LoadInt32(&queryCallCount) != 3 {
		t.Fatalf("query call count = %d, want 3", queryCallCount)
	}
	if len(result.Iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(result.Iterations))
	}
	if result.LLMCalls != 0 { // RETRY never reaches the evaluator
		t.Fatalf("llm calls = %d, want 0", result.LLMCalls)
	}
}

func TestRunAgenticLoop_CorrectiveGradingLowFractionCorrectIsBoundedByMaxIterations(t *testing.T) {
	var queryCallCount int32
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&queryCallCount, 1)
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1"), hitJSON("c2")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	// An adversarial grader that never reports confidence.
	graderFn := func(_ context.Context, _ string, hits []RagHit) ([]HitGrade, error) {
		grades := make([]HitGrade, len(hits))
		for i := range grades {
			grades[i] = HitGradeIncorrect
		}
		return grades, nil
	}

	result, err := RunAgenticLoop(context.Background(), rag, "adversarial query", "DocumentChunk", RunAgenticLoopOptions{
		Purpose:       "research",
		EmbeddingFn:   embeddingFnForScore(0.7), // lands in Evaluate
		GraderFn:      graderFn,
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("RunAgenticLoop: %v", err)
	}
	if result.StoppedReason != "max_iterations" {
		t.Fatalf("stopped reason = %q, want max_iterations", result.StoppedReason)
	}
	if atomic.LoadInt32(&queryCallCount) != 3 {
		t.Fatalf("query call count = %d, want 3", queryCallCount)
	}
	if result.LLMCalls != 3 { // one grading call per iteration, no HyDE
		t.Fatalf("llm calls = %d, want 3", result.LLMCalls)
	}
}

func TestRunAgenticLoop_NoGraderConfiguredStopsImmediatelyOnEvaluate(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunAgenticLoop(context.Background(), rag, "q", "DocumentChunk", RunAgenticLoopOptions{
		Purpose:     "research",
		EmbeddingFn: embeddingFnForScore(0.7),
	})
	if err != nil {
		t.Fatalf("RunAgenticLoop: %v", err)
	}
	if result.StoppedReason != "no_grader_configured" {
		t.Fatalf("stopped reason = %q, want no_grader_configured", result.StoppedReason)
	}
	if result.LLMCalls != 0 || len(result.Iterations) != 1 {
		t.Fatalf("got llm_calls=%d iterations=%d, want 0/1", result.LLMCalls, len(result.Iterations))
	}
}

// ── RunAgenticLoop — corrective grading reaching loop confidence ───────────

func TestRunAgenticLoop_CorrectiveGradingConfidentStop(t *testing.T) {
	var queryCallCount int32
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&queryCallCount, 1)
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1"), hitJSON("c2")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	graderFn := func(_ context.Context, _ string, hits []RagHit) ([]HitGrade, error) {
		grades := make([]HitGrade, len(hits))
		for i := range grades {
			grades[i] = HitGradeCorrect
		}
		return grades, nil
	}

	result, err := RunAgenticLoop(context.Background(), rag, "q", "DocumentChunk", RunAgenticLoopOptions{
		Purpose:     "research",
		EmbeddingFn: embeddingFnForScore(0.7),
		GraderFn:    graderFn,
	})
	if err != nil {
		t.Fatalf("RunAgenticLoop: %v", err)
	}
	if result.StoppedReason != "confident" {
		t.Fatalf("stopped reason = %q, want confident", result.StoppedReason)
	}
	if atomic.LoadInt32(&queryCallCount) != 1 || result.LLMCalls != 1 {
		t.Fatalf("got query_calls=%d llm_calls=%d, want 1/1", queryCallCount, result.LLMCalls)
	}
	last := result.Iterations[len(result.Iterations)-1]
	if last.Confidence == nil || math.Abs(*last.Confidence-1.0) > 1e-9 || *last.Confidence < LoopConfidenceThreshold {
		t.Fatalf("last iteration confidence = %v, want >= threshold", last.Confidence)
	}
}

// ── RunAgenticLoop — web-search fallback + HyDE requery refinement ─────────

func TestRunAgenticLoop_WebSearchFallbackUsedWhenNeedsRequery(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	graderFn := func(_ context.Context, _ string, hits []RagHit) ([]HitGrade, error) {
		grades := make([]HitGrade, len(hits))
		for i := range grades {
			grades[i] = HitGradeIncorrect
		}
		return grades, nil
	}

	var fallbackCalls []string
	webSearchFallback := func(_ context.Context, q string) (*RagQueryResponse, error) {
		fallbackCalls = append(fallbackCalls, q)
		return &RagQueryResponse{Hits: []RagHit{understandingTestHit("web-1")}}, nil
	}

	result, err := RunAgenticLoop(context.Background(), rag, "q", "DocumentChunk", RunAgenticLoopOptions{
		Purpose:           "research",
		EmbeddingFn:       embeddingFnForScore(0.7),
		GraderFn:          graderFn,
		WebSearchFallback: webSearchFallback,
		MaxIterations:     5,
	})
	if err != nil {
		t.Fatalf("RunAgenticLoop: %v", err)
	}
	if result.StoppedReason != "web_search_fallback" {
		t.Fatalf("stopped reason = %q, want web_search_fallback", result.StoppedReason)
	}
	if len(fallbackCalls) != 1 || fallbackCalls[0] != "q" {
		t.Fatalf("fallback calls = %v, want [q]", fallbackCalls)
	}
	if len(result.Response.Hits) != 1 || result.Response.Hits[0].ChunkID != "web-1" {
		t.Fatalf("response hits = %v", result.Response.Hits)
	}
	if result.LLMCalls != 1 { // the one grading call; fallback isn't an LLM call
		t.Fatalf("llm calls = %d, want 1", result.LLMCalls)
	}
}

func TestRunAgenticLoop_HydeRefinesQueryOnRequeryAndIsCalledAtMostOncePerRetry(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("c1")}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	graderFn := func(_ context.Context, _ string, hits []RagHit) ([]HitGrade, error) {
		grades := make([]HitGrade, len(hits))
		for i := range grades {
			grades[i] = HitGradeIncorrect
		}
		return grades, nil
	}
	var hydeCalls []string
	hypothesisFn := func(_ context.Context, q string) (string, error) {
		hydeCalls = append(hydeCalls, q)
		return "a refined hypothetical answer, not numeric-intent", nil
	}

	result, err := RunAgenticLoop(context.Background(), rag, "How does hybrid retrieval work?", "DocumentChunk", RunAgenticLoopOptions{
		Purpose:       "research",
		EmbeddingFn:   embeddingFnForScore(0.7),
		GraderFn:      graderFn,
		HypothesisFn:  hypothesisFn,
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatalf("RunAgenticLoop: %v", err)
	}
	if result.StoppedReason != "max_iterations" {
		t.Fatalf("stopped reason = %q, want max_iterations", result.StoppedReason)
	}
	if len(result.Iterations) != 2 {
		t.Fatalf("iterations = %d, want 2", len(result.Iterations))
	}
	// HyDE only fires once — after iteration 0's low-confidence grading,
	// not again on the last iteration (which returns before requerying
	// further).
	if len(hydeCalls) != 1 || hydeCalls[0] != "How does hybrid retrieval work?" {
		t.Fatalf("hyde calls = %v, want exactly one call with the original query", hydeCalls)
	}
	if result.LLMCalls != 3 { // 2 grading calls + 1 HyDE call
		t.Fatalf("llm calls = %d, want 3", result.LLMCalls)
	}
	if result.Iterations[1].Query != "a refined hypothetical answer, not numeric-intent" {
		t.Fatalf("iteration 1 query = %q", result.Iterations[1].Query)
	}
}

func TestRunAgenticLoop_RejectsNonPositiveMaxIterations(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	// Go's zero-value convention treats MaxIterations=0 as "use the
	// default" (see the file doc comment), so the adversarial-validation
	// case uses an explicit negative value instead of Python's 0.
	_, err := RunAgenticLoop(context.Background(), rag, "q", "DocumentChunk", RunAgenticLoopOptions{
		Purpose:       "research",
		EmbeddingFn:   embeddingFnForScore(1.0),
		MaxIterations: -1,
	})
	if err == nil {
		t.Fatal("RunAgenticLoop: want error for negative MaxIterations, got nil")
	}
}

// ── sub-agent fan-out — verified default thresholds ─────────────────────────

func fanoutStrategies() []SubAgentStrategy {
	return []SubAgentStrategy{
		{Name: "lexical", RagOptions: SmartRagOptions{SearchMode: "lexical"}},
		{Name: "dense", RagOptions: SmartRagOptions{SearchMode: "dense"}},
		{Name: "hybrid", RagOptions: SmartRagOptions{SearchMode: "hybrid"}},
	}
}

func TestRagFanout_ThresholdDefaultsMatchVerifiedValues(t *testing.T) {
	if LowConfidenceFloor != 0.20 || MergeThreshold != 0.50 || MinFanoutStrategies != 2 || MaxFanoutStrategies != 5 {
		t.Fatalf("fanout threshold defaults drifted: floor=%v merge=%v min=%v max=%v",
			LowConfidenceFloor, MergeThreshold, MinFanoutStrategies, MaxFanoutStrategies)
	}
}

// ── deterministic winner selection (AC #1) ──────────────────────────────────

func TestRunSubagentFanout_WinnerIsStrictArgmaxByConfidenceNoLLMCall(t *testing.T) {
	confidences := map[string]float64{"lexical": 0.9, "dense": 0.3, "hybrid": 0.1}
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			mode := body["search_mode"].(string)
			writeJSON(w, 200, map[string]any{"hits": []any{fanoutHitJSON(mode+"-1", f64(confidences[mode]))}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies(), RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	// Deterministic argmax — no arbitration callable exists anywhere in
	// this call, so "no LLM call in the merge path" is provable by
	// construction: RunSubagentFanout's signature accepts no LLM-shaped
	// callable at all.
	if result.Winner.Strategy.Name != "lexical" {
		t.Fatalf("winner = %q, want lexical", result.Winner.Strategy.Name)
	}
	if math.Abs(result.Winner.Confidence-0.9) > 1e-9 {
		t.Fatalf("winner confidence = %v, want 0.9", result.Winner.Confidence)
	}
}

func TestRunSubagentFanout_TiesBrokenByStrategyDeclarationOrder(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{fanoutHitJSON("c1", f64(0.9))}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies(), RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	if result.Winner.Strategy.Name != "lexical" { // lexical, dense, hybrid — all score 0.9
		t.Fatalf("winner = %q, want lexical (declaration order)", result.Winner.Strategy.Name)
	}
}

// ── LowConfidenceFloor — excluded as noise before any expensive step ───────

func TestRunSubagentFanout_LowConfidenceStrategiesExcludedAsNoise(t *testing.T) {
	confidences := map[string]float64{"lexical": 0.9, "dense": 0.1, "hybrid": 0.05}
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			mode := body["search_mode"].(string)
			writeJSON(w, 200, map[string]any{"hits": []any{fanoutHitJSON(mode+"-1", f64(confidences[mode]))}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies(), RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	if len(result.Excluded) != 2 {
		t.Fatalf("excluded = %d, want 2", len(result.Excluded))
	}
	for _, r := range result.Excluded {
		if r.Strategy.Name != "dense" && r.Strategy.Name != "hybrid" {
			t.Fatalf("unexpected excluded strategy %q", r.Strategy.Name)
		}
	}
	if len(result.Included) != 1 || result.Included[0].Strategy.Name != result.Winner.Strategy.Name {
		t.Fatalf("included = %v, want just the winner", result.Included)
	}
}

func TestRunSubagentFanout_AllStrategiesBelowFloorFallsBackToBestScoring(t *testing.T) {
	confidences := map[string]float64{"lexical": 0.15, "dense": 0.1, "hybrid": 0.05}
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			mode := body["search_mode"].(string)
			writeJSON(w, 200, map[string]any{"hits": []any{fanoutHitJSON(mode+"-1", f64(confidences[mode]))}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies(), RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	if result.Winner.Strategy.Name != "lexical" {
		t.Fatalf("winner = %q, want lexical", result.Winner.Strategy.Name)
	}
	if len(result.Excluded) != 2 {
		t.Fatalf("excluded = %d, want 2", len(result.Excluded))
	}
}

// ── MergeThreshold — supporting evidence folded in via RRF merge ───────────

func TestRunSubagentFanout_SurvivorAboveMergeThresholdIsFoldedIn(t *testing.T) {
	confidences := map[string]float64{"lexical": 0.9, "dense": 0.6, "hybrid": 0.05}
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			mode := body["search_mode"].(string)
			writeJSON(w, 200, map[string]any{"hits": []any{fanoutHitJSON(mode+"-1", f64(confidences[mode]))}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies(), RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	if len(result.Included) != 2 {
		t.Fatalf("included = %d, want 2", len(result.Included))
	}
	names := map[string]bool{}
	for _, r := range result.Included {
		names[r.Strategy.Name] = true
	}
	if !names["lexical"] || !names["dense"] {
		t.Fatalf("included strategies = %v, want lexical+dense", names)
	}
	mergedIDs := map[string]bool{}
	for _, h := range result.MergedResponse.Hits {
		mergedIDs[h.ChunkID] = true
	}
	if !mergedIDs["lexical-1"] || !mergedIDs["dense-1"] || len(mergedIDs) != 2 {
		t.Fatalf("merged ids = %v, want {lexical-1, dense-1}", mergedIDs)
	}
}

func TestRunSubagentFanout_SurvivorBelowMergeThresholdIsNotFoldedIn(t *testing.T) {
	confidences := map[string]float64{"lexical": 0.9, "dense": 0.4, "hybrid": 0.3}
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			mode := body["search_mode"].(string)
			writeJSON(w, 200, map[string]any{"hits": []any{fanoutHitJSON(mode+"-1", f64(confidences[mode]))}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies(), RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	if len(result.Included) != 1 || result.Included[0].Strategy.Name != "lexical" {
		t.Fatalf("included = %v, want just lexical", result.Included)
	}
	if result.MergedResponse != result.Winner.Response {
		t.Fatal("merged response should be the winner's response unchanged")
	}
	if len(result.MergedResponse.Hits) != 1 || result.MergedResponse.Hits[0].ChunkID != "lexical-1" {
		t.Fatalf("merged hits = %v", result.MergedResponse.Hits)
	}
}

// ── missing relevance_confidence (pre-#4520 server) defaults to 0.0 ────────

func TestRunSubagentFanout_MissingRelevanceConfidenceDefaultsToZeroAndIsExcluded(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{fanoutHitJSON("c1", nil)}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies(), RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	if result.Winner.Confidence != 0.0 {
		t.Fatalf("winner confidence = %v, want 0.0", result.Winner.Confidence)
	}
	// All three strategies tie at 0.0 (below the floor) -> fallback keeps
	// one winner and excludes the other two, never crashes on an
	// all-noise set.
	if len(result.Excluded) != 2 {
		t.Fatalf("excluded = %d, want 2", len(result.Excluded))
	}
}

func TestRunSubagentFanout_EmptyHitsScoresZeroConfidence(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	result, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies(), RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	if result.Winner.Confidence != 0.0 {
		t.Fatalf("winner confidence = %v, want 0.0", result.Winner.Confidence)
	}
}

// ── strategy-count validation ───────────────────────────────────────────────

func TestRunSubagentFanout_RejectsFewerThanTwoStrategies(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	_, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", fanoutStrategies()[:1], RunSubagentFanoutOptions{Purpose: "research"})
	if err == nil {
		t.Fatal("RunSubagentFanout: want error for < 2 strategies, got nil")
	}
}

func TestRunSubagentFanout_RejectsMoreThanFiveStrategies(t *testing.T) {
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"hits": []any{}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	six := make([]SubAgentStrategy, 6)
	for i := range six {
		six[i] = SubAgentStrategy{Name: "s", RagOptions: SmartRagOptions{SearchMode: "hybrid"}}
	}
	_, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", six, RunSubagentFanoutOptions{Purpose: "research"})
	if err == nil {
		t.Fatal("RunSubagentFanout: want error for > 5 strategies, got nil")
	}
}

// ── every strategy issues exactly one /rag/query call, nothing more ────────

func TestRunSubagentFanout_EachStrategyIssuesExactlyOneCall(t *testing.T) {
	var callCount int32
	srv := routedServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&callCount, 1)
			writeJSON(w, 200, map[string]any{"hits": []any{fanoutHitJSON("c1", f64(0.9))}})
		},
	})
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{})
	rag := NewRagClient(c)

	strategies := fanoutStrategies()
	_, err := RunSubagentFanout(context.Background(), rag, "q", "DocumentChunk", strategies, RunSubagentFanoutOptions{Purpose: "research"})
	if err != nil {
		t.Fatalf("RunSubagentFanout: %v", err)
	}
	if int(atomic.LoadInt32(&callCount)) != len(strategies) {
		t.Fatalf("call count = %d, want %d", callCount, len(strategies))
	}
}
