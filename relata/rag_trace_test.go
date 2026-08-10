package relata

import (
	"context"
	"testing"
)

// Tests for reasoning-trace write-back (#4528) — the final SDK-side stage of
// the RAG epic's agentic loop.
//
// Mirrors sdks/python/tests/test_rag_trace.py's acceptance criteria:
// constructs real LoopResult/FanoutResult/SynthesisResult objects (the same
// types those modules' own real execution produces) rather than mocking
// them, then asserts the exact rag_store_answer MCP payload
// BuildReasoningTrace/StoreReasoningTrace builds from them. Reuses
// mcpCapture/argsOf from mcp_wrapper_test.go (same package).

func ragTraceTestHit(chunkID string, relevanceConfidence *float64) RagHit {
	return RagHit{
		BM25Score:           1.0,
		VectorScore:         1.0,
		ChunkID:             chunkID,
		ReportID:            "doc-1",
		Text:                "text for " + chunkID,
		SectionPath:         []string{"3", "3.2"},
		PageStart:           4,
		PageEnd:             5,
		RelevanceConfidence: relevanceConfidence,
	}
}

func ragTraceTestCitation(chunkID string) Citation {
	return Citation{
		ChunkID:     chunkID,
		ReportID:    "doc-1",
		SectionPath: []string{"3", "3.2"},
		PageStart:   4,
		PageEnd:     5,
	}
}

// twoIterationLoop builds a loop that evaluated once (grading found one
// incorrect hit) and then converged with confidence — the shape the
// corrective-retrieval tier (#4525) produces when it forces exactly one
// re-query.
func twoIterationLoop() *LoopResult {
	hitA := ragTraceTestHit("ch_1", f64(0.4))
	hitB := ragTraceTestHit("ch_2", f64(0.2))
	hitC := ragTraceTestHit("ch_3", f64(0.9))

	conf0 := 0.5
	it0 := LoopIteration{
		Query:    "original query",
		Response: &RagQueryResponse{Hits: []RagHit{hitA, hitB}},
		Gate:     &HeuristicGateResult{Score: 0.7, Decision: GateDecisionEvaluate},
		Grading: &CorrectiveGradingResult{
			Grades:          []HitGrade{HitGradeCorrect, HitGradeIncorrect},
			FractionCorrect: 0.5,
		},
		Confidence: &conf0,
	}
	conf1 := 0.92
	it1 := LoopIteration{
		Query:      "original query, refined",
		Response:   &RagQueryResponse{Hits: []RagHit{hitC}},
		Gate:       &HeuristicGateResult{Score: 0.92, Decision: GateDecisionPass},
		Confidence: &conf1,
	}
	return &LoopResult{
		Response:      it1.Response,
		Iterations:    []LoopIteration{it0, it1},
		LLMCalls:      1,
		StoppedReason: "heuristic_pass",
	}
}

func ragTraceSynthesisResult() *SynthesisResult {
	sentence := SynthesizedSentence{
		Text:      "Answer text grounded in [ch_3].",
		Citations: []Citation{ragTraceTestCitation("ch_3")},
		Supported: true,
	}
	return &SynthesisResult{
		Answer:           "Answer text grounded in [ch_3].",
		Sentences:        []SynthesizedSentence{sentence},
		Citations:        []Citation{ragTraceTestCitation("ch_3")},
		UnsupportedCount: 0,
	}
}

// ---------------------------------------------------------------------------
// BuildReasoningTrace — pure payload-building assertions
// ---------------------------------------------------------------------------

func TestBuildReasoningTrace_IterationCountAndFinalConfidence(t *testing.T) {
	loopResult := twoIterationLoop()
	args := BuildReasoningTrace("q", loopResult, ragTraceSynthesisResult(), nil)
	if args["num_iterations"] != 2 {
		t.Fatalf("num_iterations = %v", args["num_iterations"])
	}
	if args["confidence"] != 0.92 { // the LAST iteration's confidence, not the first
		t.Fatalf("confidence = %v", args["confidence"])
	}
}

func TestBuildReasoningTrace_IterationTraceCarriesPerIterationConfidenceAndQuery(t *testing.T) {
	loopResult := twoIterationLoop()
	args := BuildReasoningTrace("q", loopResult, ragTraceSynthesisResult(), nil)
	trace, ok := args["iteration_trace"].([]map[string]any)
	if !ok || len(trace) != 2 {
		t.Fatalf("iteration_trace = %v", args["iteration_trace"])
	}
	if trace[0]["query"] != "original query" || trace[0]["confidence"] != 0.5 {
		t.Fatalf("trace[0] = %v", trace[0])
	}
	if trace[0]["gate_decision"] != "evaluate" || trace[0]["fraction_correct"] != 0.5 {
		t.Fatalf("trace[0] = %v", trace[0])
	}
	if trace[1]["query"] != "original query, refined" || trace[1]["confidence"] != 0.92 {
		t.Fatalf("trace[1] = %v", trace[1])
	}
	if trace[1]["gate_decision"] != "pass" {
		t.Fatalf("trace[1] gate_decision = %v", trace[1]["gate_decision"])
	}
	if _, present := trace[1]["fraction_correct"]; present {
		t.Fatalf("trace[1] must not carry fraction_correct: %v", trace[1])
	}
}

func TestBuildReasoningTrace_EvidenceGapsIncludeHitsGradedNotCorrect(t *testing.T) {
	loopResult := twoIterationLoop()
	args := BuildReasoningTrace("q", loopResult, ragTraceSynthesisResult(), nil)
	gaps, ok := args["evidence_gaps"].([]map[string]any)
	if !ok || len(gaps) != 1 {
		t.Fatalf("evidence_gaps = %v", args["evidence_gaps"])
	}
	if gaps[0]["chunk_id"] != "ch_2" || gaps[0]["grade"] != "incorrect" || gaps[0]["reason"] != "corrective_grading" {
		t.Fatalf("gaps[0] = %v", gaps[0])
	}
	for _, g := range gaps {
		if g["chunk_id"] == "ch_1" {
			t.Fatalf("CORRECT-graded hit ch_1 must not show up as a gap: %v", gaps)
		}
	}
}

func TestBuildReasoningTrace_EvidenceGapsIncludeFanoutExcludedStrategies(t *testing.T) {
	loopResult := twoIterationLoop()
	winner := SubAgentResult{
		Strategy:   SubAgentStrategy{Name: "hybrid"},
		Response:   &RagQueryResponse{Hits: []RagHit{ragTraceTestHit("ch_3", nil)}},
		Confidence: 0.9,
	}
	excluded := SubAgentResult{
		Strategy:   SubAgentStrategy{Name: "lexical"},
		Response:   &RagQueryResponse{Hits: nil},
		Confidence: 0.05,
	}
	fanoutResult := &FanoutResult{
		Winner:         winner,
		MergedResponse: winner.Response,
		Included:       []SubAgentResult{winner},
		Excluded:       []SubAgentResult{excluded},
	}
	args := BuildReasoningTrace("q", loopResult, ragTraceSynthesisResult(), &BuildReasoningTraceOptions{FanoutResult: fanoutResult})
	gaps, ok := args["evidence_gaps"].([]map[string]any)
	if !ok {
		t.Fatalf("evidence_gaps = %v", args["evidence_gaps"])
	}
	var fanoutGaps []map[string]any
	for _, g := range gaps {
		if g["reason"] == "fanout_low_confidence" {
			fanoutGaps = append(fanoutGaps, g)
		}
	}
	if len(fanoutGaps) != 1 {
		t.Fatalf("fanout gaps = %v", fanoutGaps)
	}
	if fanoutGaps[0]["strategy"] != "lexical" || fanoutGaps[0]["confidence"] != 0.05 {
		t.Fatalf("fanoutGaps[0] = %v", fanoutGaps[0])
	}
}

func TestBuildReasoningTrace_EvidenceGapsEmptyWhenNothingDiscarded(t *testing.T) {
	conf := 0.9
	it := LoopIteration{
		Query:      "q",
		Response:   &RagQueryResponse{Hits: []RagHit{ragTraceTestHit("ch_1", nil)}},
		Gate:       &HeuristicGateResult{Score: 0.9, Decision: GateDecisionPass},
		Confidence: &conf,
	}
	loopResult := &LoopResult{Response: it.Response, Iterations: []LoopIteration{it}, StoppedReason: "heuristic_pass"}
	args := BuildReasoningTrace("q", loopResult, ragTraceSynthesisResult(), nil)
	gaps, ok := args["evidence_gaps"].([]map[string]any)
	if !ok || len(gaps) != 0 {
		t.Fatalf("evidence_gaps = %v", args["evidence_gaps"])
	}
}

func TestBuildReasoningTrace_SourcesBuiltFromSynthesisCitationsWithRelevanceScore(t *testing.T) {
	loopResult := twoIterationLoop()
	args := BuildReasoningTrace("q", loopResult, ragTraceSynthesisResult(), nil)
	sources, ok := args["sources"].([]map[string]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("sources = %v", args["sources"])
	}
	s := sources[0]
	if s["id"] != "ch_3" || s["source"] != "doc-1" || s["score"] != 0.9 || s["page"] != 4 {
		t.Fatalf("sources[0] = %v", s)
	}
	sectionPath, ok := s["sectionPath"].([]string)
	if !ok || len(sectionPath) != 2 || sectionPath[0] != "3" || sectionPath[1] != "3.2" {
		t.Fatalf("sectionPath = %v", s["sectionPath"])
	}
}

func TestBuildReasoningTrace_FaithfulnessComputedFromUnsupportedFraction(t *testing.T) {
	loopResult := twoIterationLoop()
	sentenceOK := SynthesizedSentence{Text: "ok [ch_3].", Citations: []Citation{ragTraceTestCitation("ch_3")}, Supported: true}
	sentenceBad := SynthesizedSentence{Text: "unsupported claim [unsupported]", Supported: false}
	synthesisResult := &SynthesisResult{
		Answer:           "ok [ch_3]. unsupported claim [unsupported]",
		Sentences:        []SynthesizedSentence{sentenceOK, sentenceBad},
		Citations:        []Citation{ragTraceTestCitation("ch_3")},
		UnsupportedCount: 1,
	}
	args := BuildReasoningTrace("q", loopResult, synthesisResult, nil)
	if args["faithfulness"] != 0.5 {
		t.Fatalf("faithfulness = %v", args["faithfulness"])
	}
}

func TestBuildReasoningTrace_FaithfulnessIsSentinelWhenNoSentences(t *testing.T) {
	loopResult := twoIterationLoop()
	empty := &SynthesisResult{}
	args := BuildReasoningTrace("q", loopResult, empty, nil)
	if args["faithfulness"] != -1.0 {
		t.Fatalf("faithfulness = %v", args["faithfulness"])
	}
}

func TestBuildReasoningTrace_OptionalCaseIDPurposeDurationOmittedWhenUnset(t *testing.T) {
	loopResult := twoIterationLoop()
	args := BuildReasoningTrace("q", loopResult, ragTraceSynthesisResult(), nil)
	if _, present := args["case_id"]; present {
		t.Fatalf("case_id must be omitted: %v", args)
	}
	if _, present := args["purpose"]; present {
		t.Fatalf("purpose must be omitted: %v", args)
	}
	if _, present := args["duration_ms"]; present {
		t.Fatalf("duration_ms must be omitted: %v", args)
	}
}

func TestBuildReasoningTrace_OptionalCaseIDPurposeDurationSentWhenGiven(t *testing.T) {
	loopResult := twoIterationLoop()
	durationMs := 1234
	args := BuildReasoningTrace("q", loopResult, ragTraceSynthesisResult(), &BuildReasoningTraceOptions{
		CaseID:     "case-42",
		Purpose:    "analytics",
		DurationMs: &durationMs,
	})
	if args["case_id"] != "case-42" || args["purpose"] != "analytics" || args["duration_ms"] != 1234 {
		t.Fatalf("args = %v", args)
	}
}

// ---------------------------------------------------------------------------
// StoreReasoningTrace — wire-call assertions
// ---------------------------------------------------------------------------

func TestStoreReasoningTrace_CallsRagStoreAnswerWithFullPayload(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	loopResult := twoIterationLoop()
	result, err := StoreReasoningTrace(context.Background(), mcp, "q", loopResult, ragTraceSynthesisResult(), &BuildReasoningTraceOptions{Purpose: "analytics"})
	if err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "rag_store_answer" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
	args := argsOf(t, gotBody)
	if args["question"] != "q" || args["answer"] != "Answer text grounded in [ch_3]." {
		t.Fatalf("args = %v", args)
	}
	if args["num_iterations"] != float64(2) {
		t.Fatalf("num_iterations = %v", args["num_iterations"])
	}
	iterationTrace, ok := args["iteration_trace"].([]any)
	if !ok || len(iterationTrace) != 2 {
		t.Fatalf("iteration_trace = %v", args["iteration_trace"])
	}
	evidenceGaps, ok := args["evidence_gaps"].([]any)
	if !ok || len(evidenceGaps) != 1 {
		t.Fatalf("evidence_gaps = %v", args["evidence_gaps"])
	}
	if args["purpose"] != "analytics" {
		t.Fatalf("purpose = %v", args["purpose"])
	}
	// The (mocked) server's response is passed straight through.
	if result["ok"] != true {
		t.Fatalf("result = %v", result)
	}
}
