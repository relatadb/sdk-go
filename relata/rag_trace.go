package relata

import "context"

// Package-level: reasoning-trace write-back — #4528, the final SDK-side
// stage of the RAG epic's agentic loop.
//
// Faithful Go port of sdks/python/relata/rag_trace.py (#4583, epic #4576),
// depended on by nothing else — it consumes the already-ported LoopResult/
// FanoutResult (rag_loop.go, #4582) and SynthesisResult (synthesis.go,
// #4579) result types.
//
// The gap this closes. POST /rag/query (#4514) already returns governed,
// citable rows, and the write path this file calls (RagStoreAnswer,
// crates/relata-cli/src/serve/mcp/doc_writes.rs) already turns a final
// answer into an auditable RagAnswer + RagSource row pair. But an agentic
// loop's *decisions* — why it re-queried (#4525's heuristic gate +
// corrective grading), what it discarded (a hit graded ambiguous/incorrect,
// a #4526 fan-out strategy excluded as noise), and how its confidence moved
// iteration to iteration — lived entirely in the SDK, never reaching
// RelataDB's audit log or provenance chain. Today you could prove *what was
// read*; you could not prove *how the answer was reached*. This file is
// purely the bridge: it takes the real objects a completed loop already
// produced and writes the full trace back via the existing rag_store_answer
// MCP tool — no new write path, two small additive fields on it
// (iteration_trace, evidence_gaps, both JSON-encoded text server-side; see
// the handler's #4528 doc comment).
//
// RelataDB has no server-side agent loop (ADR-013): this file makes no LLM
// call and issues no /rag/query call of its own — it is pure bookkeeping
// over results the caller already produced.
//
// One structural adaptation from the Python source, required by language
// shape rather than a deliberate behavior change: Python exposes separate
// sync (store_reasoning_trace) and async (astore_reasoning_trace) twins
// because its McpClient has two transports. Go has one calling convention
// (a context.Context-taking function that may block), so both collapse to
// the single StoreReasoningTrace here — the same collapse rag_loop.go and
// rag_understanding.go already made.
//
// Payload shape: like mcp.go's existing RagStoreAnswer wrapper,
// BuildReasoningTrace/StoreReasoningTrace build a map[string]any rather
// than a typed struct — the rag_store_answer argument map is the literal
// wire payload (snake_case keys, one field — sourcePath below —
// deliberately camelCase to match the handler's source-row field), not a
// TS/Go-idiomatic intermediate type.

// iterationTraceFor builds one entry per loop iteration ("iteration" (int),
// "query" (string), "confidence" (float64 or nil), "hit_count" (int), and,
// when available, "gate_decision" (string), "gate_score" (float64),
// "fraction_correct" (float64), "graded_hit_count" (int)): query text, this
// iteration's confidence signal (LoopIteration.Confidence — the heuristic
// gate's score when no grading ran, else corrective grading's
// FractionCorrect), and, when available, the raw gate/grading signals
// behind it. This is #4528's "per-iteration confidence" acceptance
// criterion.
func iterationTraceFor(loopResult *LoopResult) []map[string]any {
	trace := make([]map[string]any, 0, len(loopResult.Iterations))
	for i, iteration := range loopResult.Iterations {
		var confidence any
		if iteration.Confidence != nil {
			confidence = *iteration.Confidence
		}
		entry := map[string]any{
			"iteration":  i,
			"query":      iteration.Query,
			"confidence": confidence,
			"hit_count":  len(iteration.Response.Hits),
		}
		if iteration.Gate != nil {
			entry["gate_decision"] = string(iteration.Gate.Decision)
			entry["gate_score"] = iteration.Gate.Score
		}
		if iteration.Grading != nil {
			entry["fraction_correct"] = iteration.Grading.FractionCorrect
			entry["graded_hit_count"] = len(iteration.Grading.Grades)
		}
		trace = append(trace, entry)
	}
	return trace
}

// evidenceGapsFor builds the "evidence_gaps" payload: hits corrective
// retrieval grading marked ambiguous/incorrect in any iteration, plus —
// when a #4526 sub-agent fan-out ran — every strategy it excluded as noise
// below LowConfidenceFloor. This is #4528's "discarded evidence" acceptance
// criterion; it stays an empty (not nil) slice when neither source has
// anything, matching the Python reference's empty-list-not-absent
// behavior.
func evidenceGapsFor(loopResult *LoopResult, fanoutResult *FanoutResult) []map[string]any {
	gaps := []map[string]any{}
	for i, iteration := range loopResult.Iterations {
		if iteration.Grading == nil {
			continue
		}
		hits := iteration.Response.Hits
		grades := iteration.Grading.Grades
		n := len(hits)
		if len(grades) < n {
			n = len(grades)
		}
		for j := 0; j < n; j++ {
			if grades[j] != HitGradeCorrect {
				gaps = append(gaps, map[string]any{
					"iteration": i,
					"chunk_id":  hits[j].ChunkID,
					"grade":     string(grades[j]),
					"reason":    "corrective_grading",
				})
			}
		}
	}
	if fanoutResult != nil {
		for _, excluded := range fanoutResult.Excluded {
			gaps = append(gaps, map[string]any{
				"strategy":   excluded.Strategy.Name,
				"confidence": excluded.Confidence,
				"reason":     "fanout_low_confidence",
			})
		}
	}
	return gaps
}

// finalConfidenceFor is the loop's final confidence signal — the last
// iteration's LoopIteration.Confidence, or 0.0 for the degenerate case of a
// loop result with no iterations recorded.
func finalConfidenceFor(loopResult *LoopResult) float64 {
	if len(loopResult.Iterations) > 0 {
		last := loopResult.Iterations[len(loopResult.Iterations)-1]
		if last.Confidence != nil {
			return *last.Confidence
		}
	}
	return 0.0
}

// faithfulnessScoreFor computes 1 - (unsupported sentence count / total),
// matching the RagAnswer.faithfulness field's documented 0-1 scale — or the
// server's own -1 "not computed" sentinel when there were no sentences to
// score (see doc_writes.rs's faith default).
func faithfulnessScoreFor(synthesisResult *SynthesisResult) float64 {
	total := len(synthesisResult.Sentences)
	if total == 0 {
		return -1.0
	}
	return 1.0 - float64(synthesisResult.UnsupportedCount)/float64(total)
}

// sourceConfidenceFor looks up citation.ChunkID's RelevanceConfidence
// (#4520) from whichever iteration's hits it came from — searched newest
// iteration first, since a re-query's hit set is the more relevant
// provenance for a repeated chunk id. 0.0 when the id is not found in any
// iteration (should not happen for a citation resolved from a real hit, but
// this function never panics on it) or the field is unset.
func sourceConfidenceFor(citation Citation, loopResult *LoopResult) float64 {
	for i := len(loopResult.Iterations) - 1; i >= 0; i-- {
		for _, hit := range loopResult.Iterations[i].Response.Hits {
			if hit.ChunkID == citation.ChunkID {
				if hit.RelevanceConfidence != nil {
					return *hit.RelevanceConfidence
				}
				return 0.0
			}
		}
	}
	return 0.0
}

// sourcesPayloadFor builds the rag_store_answer "sources" array from the
// synthesis result's deduplicated, by-construction-real citations (#4579)
// — never from raw LLM output, so a fabricated source cannot reach this
// payload any more than it can reach SynthesisResult.Citations itself.
func sourcesPayloadFor(synthesisResult *SynthesisResult, loopResult *LoopResult) []map[string]any {
	sources := make([]map[string]any, 0, len(synthesisResult.Citations))
	for _, citation := range synthesisResult.Citations {
		sources = append(sources, map[string]any{
			"id":     citation.ChunkID,
			"source": citation.ReportID,
			"score":  sourceConfidenceFor(citation, loopResult),
			"page":   citation.PageStart,
			// Wire key is literally "sectionPath" (camelCase) — matches the
			// rag_store_answer handler's source-row field, not this file's
			// own snake_case convention for the rest of the payload.
			"sectionPath": citation.SectionPath,
		})
	}
	return sources
}

// BuildReasoningTraceOptions configures BuildReasoningTrace/
// StoreReasoningTrace.
type BuildReasoningTraceOptions struct {
	// FanoutResult is the #4526 sub-agent fan-out result, when one ran.
	FanoutResult *FanoutResult
	CaseID       string
	Purpose      string
	// DurationMs is a pointer so an explicit 0ms duration (a valid value)
	// is distinguishable from "not given" — same convention as
	// RecallOptions' quality-gate pointer fields.
	DurationMs *int
}

// BuildReasoningTrace builds the full rag_store_answer argument map from a
// completed #4525 loop + #4579 synthesis result (and, when a #4526 fan-out
// ran, its FanoutResult): iteration count, per-iteration confidence,
// discarded evidence, and final citations — not just the final answer
// text.
//
// Exposed separately from StoreReasoningTrace so a caller can inspect or
// log the exact payload before it goes over the wire.
func BuildReasoningTrace(query string, loopResult *LoopResult, synthesisResult *SynthesisResult, opts *BuildReasoningTraceOptions) map[string]any {
	var fanoutResult *FanoutResult
	if opts != nil {
		fanoutResult = opts.FanoutResult
	}
	args := map[string]any{
		"question":        query,
		"answer":          synthesisResult.Answer,
		"confidence":      finalConfidenceFor(loopResult),
		"num_iterations":  len(loopResult.Iterations),
		"faithfulness":    faithfulnessScoreFor(synthesisResult),
		"sources":         sourcesPayloadFor(synthesisResult, loopResult),
		"iteration_trace": iterationTraceFor(loopResult),
		"evidence_gaps":   evidenceGapsFor(loopResult, fanoutResult),
	}
	if opts != nil {
		// Non-empty-string checks (not merely "opts.CaseID present") mirror
		// the Python reference's `if case_id:`/`if purpose:` — an empty
		// string is omitted the same as unset. DurationMs is the one
		// exception: 0 is a valid duration, so it uses a nil-pointer check
		// instead.
		if opts.CaseID != "" {
			args["case_id"] = opts.CaseID
		}
		if opts.Purpose != "" {
			args["purpose"] = opts.Purpose
		}
		if opts.DurationMs != nil {
			args["duration_ms"] = *opts.DurationMs
		}
	}
	return args
}

// StoreReasoningTrace writes the full reasoning trace back via the existing
// rag_store_answer MCP tool (crates/relata-cli/src/serve/mcp/doc_writes.rs)
// — the SDK-side half of #4528. Call this at the end of the loop
// (#4525/#4526's call site, after #4579's Synthesize), not mocked: the
// written row carries the real iteration/confidence/evidence trail this
// specific run produced.
//
// Returns the unwrapped MCP result map — e.g. {"stored": "RagAnswer",
// "source_rows": N, "has_iteration_trace": true, "evidence_gap_count": M,
// ...}.
func StoreReasoningTrace(ctx context.Context, mcpClient *McpClient, query string, loopResult *LoopResult, synthesisResult *SynthesisResult, opts *BuildReasoningTraceOptions) (map[string]any, error) {
	args := BuildReasoningTrace(query, loopResult, synthesisResult, opts)
	return mcpClient.CallTool(ctx, "rag_store_answer", args)
}
