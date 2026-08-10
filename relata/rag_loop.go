package relata

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
)

// Package-level: the agentic loop's spine — heuristic gate, corrective
// retrieval grading, and loop confidence evaluation (#4525, the RAG epic's
// three-tier cost ladder), plus sub-agent fan-out with a deterministic
// (non-LLM) merge (#4526).
//
// Faithful Go port of sdks/python/relata/rag_loop.py (#4582, epic #4576) —
// closing the deliberate Python-only asymmetry ADR-0298 introduced ("one
// SDK carries the canonical implementation... until real usage demands
// porting the loop to them too"). Every verified numeric constant (the
// seven fidelity thresholds below) matches the Python source exactly.
//
// Structural adaptations from the Python source, required by language shape
// rather than a deliberate behavior change:
//
//   - Python exposes separate sync (run_agentic_loop/run_subagent_fanout)
//     and async (arun_agentic_loop/arun_subagent_fanout) twins. Go has one
//     calling convention (a context.Context-taking function that may
//     block), so each pair collapses to one function here — the same
//     collapse rag_understanding.go already made for SmartRagQuery.
//   - RunAgenticLoopOptions.MaxIterations and RunSubagentFanoutOptions.
//     MergeThreshold are plain int/float64 (zero meaning "use the verified
//     default"), not pointers — unlike SmartRagOptions.TopK/ExpandWindow,
//     which need pointers to preserve Python's dict-based "caller didn't
//     set this" distinction. Nobody has a legitimate reason to request
//     MaxIterations=0 or MergeThreshold=0.0 (a zero-iteration loop can never
//     run; a zero merge-threshold means "merge everything"), so this
//     mirrors RagQueryRequest's own TopK `json:"top_k,omitempty"` "0 means
//     server default" convention instead. Python's one behavioral edge case
//     this simplification loses — run_agentic_loop(max_iterations=0) raising
//     ValueError — has no Go equivalent; RunAgenticLoop validates negative
//     values instead (see its doc comment).
//   - RunSubagentFanout's per-strategy fan-out runs via goroutines + a
//     WaitGroup rather than Python's ThreadPoolExecutor — Go has no
//     thread-pool-size knob to mirror the sync twin's max_workers, the same
//     adaptation SmartRagQuery already documents.
//
// Per ADR-0298, RelataDB has no server-side agent loop and orchestration
// lives in the SDK; sdks/python/relata/rag.go's RagClient stays canonical
// and this package's RagClient (rag.go) stays a thin pass-through over the
// frozen /rag/query contract. This file is the SDK-side control flow built
// on top of that thin pass-through, now shipped here too per epic #4576.
//
// This module never calls an LLM itself: every LLM-backed decision point
// (EmbeddingFn excepted — the heuristic gate is not an LLM call either way)
// is a caller-supplied callable, exactly like rag_understanding.go's
// HypothesisGenerator.

// ---------------------------------------------------------------------------
// Verified defaults — the cost ladder's thresholds (#4525) + fan-out's
// (#4526). Do not change without a test-backed reason.
// ---------------------------------------------------------------------------

const (
	// HeuristicPassThreshold is the heuristic-gate auto-pass bar. Centroid
	// cosine strictly greater than this skips corrective grading entirely
	// — zero LLM calls.
	HeuristicPassThreshold = 0.85

	// HeuristicRetryThreshold is the heuristic-gate auto-retry floor.
	// Centroid cosine strictly less than this re-queries without ever
	// reaching corrective grading either — still zero LLM calls.
	HeuristicRetryThreshold = 0.60

	// LoopConfidenceThreshold is the loop confidence threshold. Once
	// corrective grading's FractionCorrect reaches this, RunAgenticLoop
	// stops iterating.
	LoopConfidenceThreshold = 0.85

	// MaxIterations is the hard cap on loop iterations, enforced
	// unconditionally (a bounded for loop, never unbounded) — no
	// adversarial input can make RunAgenticLoop spin forever.
	MaxIterations = 5

	// CorrectiveFractionCorrectFloor is the FractionCorrect floor below
	// which corrective grading marks CorrectiveGradingResult.NeedsRequery.
	// Not one of the epic's three named thresholds (the ticket describes
	// the *behavior* — "low fraction correct forces a re-query" — without
	// pinning an exact number); chosen as the natural "more wrong than
	// right" boundary and isolated in its own constant so a future
	// RAG-quality-gate finding can retune it without touching the three
	// verified thresholds above.
	CorrectiveFractionCorrectFloor = 0.5

	// LowConfidenceFloor: below this per-strategy confidence, a sub-agent
	// result is excluded entirely as noise before the winner comparison
	// runs at all — verified default from the reference platform system.
	LowConfidenceFloor = 0.20

	// MergeThreshold: a non-winning survivor scoring at or above this
	// confidence is folded into the merged result as supporting evidence;
	// below it, only the winner's hits are returned. Deliberately below
	// any implicit "clear winner" bar — strong enough to merge, not strong
	// enough to contend for winner on its own — verified default from the
	// reference platform system.
	MergeThreshold = 0.50

	// MinFanoutStrategies and MaxFanoutStrategies bound RunSubagentFanout:
	// at least 2 strategies (below that there is nothing to fan out or
	// merge) and at most 5 (the verified reference-system range).
	MinFanoutStrategies = 2
	MaxFanoutStrategies = 5
)

// ---------------------------------------------------------------------------
// 1. Heuristic gate
// ---------------------------------------------------------------------------

// GateDecision is the outcome of HeuristicGate.
type GateDecision string

const (
	// GateDecisionPass: score > HeuristicPassThreshold — stop, synthesize
	// from these hits. No LLM call.
	GateDecisionPass GateDecision = "pass"
	// GateDecisionRetry: score < HeuristicRetryThreshold — issue another
	// /rag/query call. No LLM call.
	GateDecisionRetry GateDecision = "retry"
	// GateDecisionEvaluate: between the two thresholds — proceed to
	// corrective retrieval grading (GradeHits), the loop's one LLM call
	// per iteration.
	GateDecisionEvaluate GateDecision = "evaluate"
)

// EmbeddingFn is a caller-supplied embedding function — RelataDB computes no
// embeddings itself (ADR-0298). Used only for the <5ms heuristic gate, never
// for an LLM call.
type EmbeddingFn func(ctx context.Context, text string) ([]float64, error)

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
	}
	for _, x := range a {
		normA += x * x
	}
	for _, x := range b {
		normB += x * x
	}
	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)
	if normA == 0.0 || normB == 0.0 {
		return 0.0
	}
	return dot / (normA * normB)
}

func embeddingCentroid(vectors [][]float64) []float64 {
	if len(vectors) == 0 {
		return nil
	}
	dim := len(vectors[0])
	sums := make([]float64, dim)
	for _, vec := range vectors {
		for i, x := range vec {
			if i < dim {
				sums[i] += x
			}
		}
	}
	n := float64(len(vectors))
	out := make([]float64, dim)
	for i, s := range sums {
		out[i] = s / n
	}
	return out
}

// HeuristicGateResult is the result of HeuristicGate.
type HeuristicGateResult struct {
	// Score is the centroid cosine similarity between the query embedding
	// and the top-K hit embeddings — 0.0 when there are no hits to compare
	// against.
	Score    float64
	Decision GateDecision
}

// HeuristicGate is the cheap pre-LLM gate: centroid cosine between
// queryEmbedding and the top-K /rag/query hits' embeddings, <5ms, no LLM
// call either way.
//
// Zero hitEmbeddings scores 0.0 and decides GateDecisionRetry — nothing to
// be confident about.
func HeuristicGate(queryEmbedding []float64, hitEmbeddings [][]float64) HeuristicGateResult {
	if len(hitEmbeddings) == 0 {
		return HeuristicGateResult{Score: 0.0, Decision: GateDecisionRetry}
	}
	score := cosineSimilarity(queryEmbedding, embeddingCentroid(hitEmbeddings))
	var decision GateDecision
	switch {
	case score > HeuristicPassThreshold:
		decision = GateDecisionPass
	case score < HeuristicRetryThreshold:
		decision = GateDecisionRetry
	default:
		decision = GateDecisionEvaluate
	}
	return HeuristicGateResult{Score: score, Decision: decision}
}

// ---------------------------------------------------------------------------
// 2. Corrective retrieval grading
// ---------------------------------------------------------------------------

// HitGrade is a per-hit grade from GradeHits' batched cheap-model call.
type HitGrade string

const (
	HitGradeCorrect   HitGrade = "correct"
	HitGradeAmbiguous HitGrade = "ambiguous"
	HitGradeIncorrect HitGrade = "incorrect"
)

// GraderFn is a caller-supplied batched grading call — RelataDB runs no LLM
// (ADR-013). Called once per grading round with (query, hits), must return
// exactly one HitGrade per hit, same order — one batched call, never one
// call per hit.
type GraderFn func(ctx context.Context, query string, hits []RagHit) ([]HitGrade, error)

// CorrectiveGradingResult is the result of GradeHits.
type CorrectiveGradingResult struct {
	// Grades holds one grade per hit, same order as the graded hit list.
	Grades []HitGrade
	// FractionCorrect is count(Correct) / len(hits) — 0.0 when there were
	// no hits to grade.
	FractionCorrect float64
}

// NeedsRequery reports whether FractionCorrect is below
// CorrectiveFractionCorrectFloor — the corrective-retrieval signal that
// forces a re-query (refined via HyDE, or a web-search fallback, or a plain
// retry — see RunAgenticLoop).
func (r CorrectiveGradingResult) NeedsRequery() bool {
	return r.FractionCorrect < CorrectiveFractionCorrectFloor
}

// GradeHits runs one batched corrective-retrieval grading call over hits.
//
// Returns an error if graderFn returns a different number of grades than
// hits given — a caller bug, not a valid partial grading.
func GradeHits(ctx context.Context, query string, hits []RagHit, graderFn GraderFn) (CorrectiveGradingResult, error) {
	if len(hits) == 0 {
		return CorrectiveGradingResult{FractionCorrect: 0.0}, nil
	}
	grades, err := graderFn(ctx, query, hits)
	if err != nil {
		return CorrectiveGradingResult{}, err
	}
	if len(grades) != len(hits) {
		return CorrectiveGradingResult{}, fmt.Errorf(
			"relata: graderFn must return exactly one grade per hit: got %d grade(s) for %d hit(s)",
			len(grades), len(hits),
		)
	}
	nCorrect := 0
	for _, g := range grades {
		if g == HitGradeCorrect {
			nCorrect++
		}
	}
	return CorrectiveGradingResult{Grades: grades, FractionCorrect: float64(nCorrect) / float64(len(hits))}, nil
}

// ---------------------------------------------------------------------------
// 3. Loop confidence + orchestration
// ---------------------------------------------------------------------------

// WebSearchFallbackFn is a caller-supplied web-search fallback, invoked when
// corrective grading's CorrectiveGradingResult.NeedsRequery trips and a
// fallback is configured. Returns a full *RagQueryResponse-shaped result —
// the loop does not care whether it came from /rag/query or an external
// search API.
type WebSearchFallbackFn func(ctx context.Context, query string) (*RagQueryResponse, error)

// LoopIteration is a record of one RunAgenticLoop iteration — the reasoning
// trace a caller writes back to its own answer/citation model is built from
// a sequence of these.
type LoopIteration struct {
	Query    string
	Response *RagQueryResponse
	Gate     *HeuristicGateResult
	Grading  *CorrectiveGradingResult
	// Confidence is this iteration's confidence signal — the heuristic
	// gate's score when no grading ran, otherwise the grading's
	// FractionCorrect.
	Confidence *float64
}

// LoopResult is the final result of RunAgenticLoop.
type LoopResult struct {
	Response   *RagQueryResponse
	Iterations []LoopIteration
	// LLMCalls is the total count of caller-supplied LLM-backed calls made
	// across every iteration (GraderFn + HyDE-refinement HypothesisFn
	// calls). 0 when the heuristic gate passed on iteration 0 — see the
	// file doc comment's zero-LLM-calls property.
	LLMCalls int
	// StoppedReason is one of "heuristic_pass", "confident",
	// "web_search_fallback", "no_grader_configured", "max_iterations".
	StoppedReason string
}

func ragOptionsToRequest(query, objectType, purpose string, opts SmartRagOptions) RagQueryRequest {
	return RagQueryRequest{
		Query:         query,
		Type:          objectType,
		Purpose:       purpose,
		TopK:          ptrInt(opts.TopK),
		Rerank:        opts.Rerank,
		SearchMode:    opts.SearchMode,
		EmbeddingSlot: opts.EmbeddingSlot,
		Filters:       opts.Filters,
		AsOf:          opts.AsOf,
		ExpandWindow:  ptrBool(opts.ExpandWindow),
		GraphHops:     opts.GraphHops,
	}
}

type loopIterationsInput struct {
	queryFn           func(ctx context.Context, text string) (*RagQueryResponse, error)
	query             string
	embeddingFn       EmbeddingFn
	graderFn          GraderFn
	hypothesisFn      HypothesisGenerator
	webSearchFallback WebSearchFallbackFn
	maxIterations     int
}

// runLoopIterations is the shared control flow behind RunAgenticLoop —
// in.queryFn is the only side-effecting call, so this function itself makes
// no other I/O decisions.
func runLoopIterations(ctx context.Context, in loopIterationsInput) (*LoopResult, error) {
	if in.maxIterations < 1 {
		return nil, errors.New("relata: maxIterations must be >= 1")
	}

	var iterations []LoopIteration
	llmCalls := 0
	currentQuery := in.query

	for iterIdx := 0; iterIdx < in.maxIterations; iterIdx++ {
		isLast := iterIdx == in.maxIterations-1
		response, err := in.queryFn(ctx, currentQuery)
		if err != nil {
			return nil, err
		}

		queryEmbedding, err := in.embeddingFn(ctx, currentQuery)
		if err != nil {
			return nil, err
		}
		hitEmbeddings := make([][]float64, len(response.Hits))
		for hitIdx, hit := range response.Hits {
			emb, err := in.embeddingFn(ctx, hit.Text)
			if err != nil {
				return nil, err
			}
			hitEmbeddings[hitIdx] = emb
		}
		gate := HeuristicGate(queryEmbedding, hitEmbeddings)
		gateCopy := gate

		if gate.Decision == GateDecisionPass {
			score := gate.Score
			iterations = append(iterations, LoopIteration{Query: currentQuery, Response: response, Gate: &gateCopy, Confidence: &score})
			return &LoopResult{Response: response, Iterations: iterations, LLMCalls: llmCalls, StoppedReason: "heuristic_pass"}, nil
		}

		if gate.Decision == GateDecisionRetry {
			score := gate.Score
			iterations = append(iterations, LoopIteration{Query: currentQuery, Response: response, Gate: &gateCopy, Confidence: &score})
			if isLast {
				return &LoopResult{Response: response, Iterations: iterations, LLMCalls: llmCalls, StoppedReason: "max_iterations"}, nil
			}
			currentQuery = in.query
			continue
		}

		// GateDecisionEvaluate — proceed to corrective retrieval grading.
		if in.graderFn == nil {
			score := gate.Score
			iterations = append(iterations, LoopIteration{Query: currentQuery, Response: response, Gate: &gateCopy, Confidence: &score})
			return &LoopResult{Response: response, Iterations: iterations, LLMCalls: llmCalls, StoppedReason: "no_grader_configured"}, nil
		}

		grading, err := GradeHits(ctx, currentQuery, response.Hits, in.graderFn)
		if err != nil {
			return nil, err
		}
		llmCalls++
		gradingCopy := grading
		fractionCorrect := grading.FractionCorrect
		iterations = append(iterations, LoopIteration{
			Query: currentQuery, Response: response, Gate: &gateCopy, Grading: &gradingCopy, Confidence: &fractionCorrect,
		})

		if grading.FractionCorrect >= LoopConfidenceThreshold {
			return &LoopResult{Response: response, Iterations: iterations, LLMCalls: llmCalls, StoppedReason: "confident"}, nil
		}

		if isLast {
			return &LoopResult{Response: response, Iterations: iterations, LLMCalls: llmCalls, StoppedReason: "max_iterations"}, nil
		}

		if grading.NeedsRequery() && in.webSearchFallback != nil {
			fallbackResponse, err := in.webSearchFallback(ctx, currentQuery)
			if err != nil {
				return nil, err
			}
			iterations = append(iterations, LoopIteration{Query: currentQuery, Response: fallbackResponse})
			return &LoopResult{Response: fallbackResponse, Iterations: iterations, LLMCalls: llmCalls, StoppedReason: "web_search_fallback"}, nil
		}

		if grading.NeedsRequery() && in.hypothesisFn != nil {
			expanded, err := ExpandQueryHyde(ctx, in.query, in.hypothesisFn)
			if err != nil {
				return nil, err
			}
			currentQuery = expanded
			llmCalls++
		} else {
			currentQuery = in.query
		}
	}

	// Unreachable: every branch above returns once isLast is true, and the
	// loop's final index always has isLast == true.
	return nil, errors.New("relata: RunAgenticLoop exhausted maxIterations without returning")
}

// RunAgenticLoopOptions configures RunAgenticLoop.
type RunAgenticLoopOptions struct {
	Purpose string
	// EmbeddingFn is the caller-supplied embedding function for the
	// heuristic gate (HeuristicGate) — not an LLM call, RelataDB computes
	// no embeddings itself (ADR-0298). Required.
	EmbeddingFn EmbeddingFn
	// GraderFn is the caller-supplied batched corrective-retrieval grader
	// (GradeHits). When nil, a query landing in the heuristic gate's
	// GateDecisionEvaluate band stops immediately with
	// StoppedReason="no_grader_configured" rather than silently skipping
	// grading.
	GraderFn GraderFn
	// HypothesisFn is the caller-supplied HyDE hypothesis generator
	// (ExpandQueryHyde, rag_understanding.go), used only to refine the
	// query when corrective grading's NeedsRequery trips.
	HypothesisFn HypothesisGenerator
	// WebSearchFallback is tried before HyDE refinement when NeedsRequery
	// trips.
	WebSearchFallback WebSearchFallbackFn
	// MaxIterations is the hard cap on loop iterations; 0 uses MaxIterations
	// (the verified default). Enforced unconditionally — the loop cannot
	// spin forever regardless of what GraderFn/EmbeddingFn return. A
	// negative value is a caller error (see the file doc comment for why
	// this isn't a pointer field like SmartRagOptions.TopK).
	MaxIterations int
	// RagOptions is forwarded to RagClient.Query on every /rag/query call.
	RagOptions SmartRagOptions
}

// RunAgenticLoop runs the three-tier cost-ladder loop: heuristic gate ->
// (optionally) corrective retrieval grading -> loop-confidence stop, up to
// MaxIterations (or opts.MaxIterations, capped the same way).
//
// Returns the final response, the full iteration history, the total
// LLM-call count (LoopResult.LLMCalls, 0 when the heuristic gate passed on
// iteration 0), and why the loop stopped.
func RunAgenticLoop(ctx context.Context, ragClient *RagClient, query, objectType string, opts RunAgenticLoopOptions) (*LoopResult, error) {
	maxIterations := opts.MaxIterations
	if maxIterations == 0 {
		maxIterations = MaxIterations
	}
	queryFn := func(ctx context.Context, text string) (*RagQueryResponse, error) {
		return ragClient.Query(ctx, ragOptionsToRequest(text, objectType, opts.Purpose, opts.RagOptions))
	}
	return runLoopIterations(ctx, loopIterationsInput{
		queryFn:           queryFn,
		query:             query,
		embeddingFn:       opts.EmbeddingFn,
		graderFn:          opts.GraderFn,
		hypothesisFn:      opts.HypothesisFn,
		webSearchFallback: opts.WebSearchFallback,
		maxIterations:     maxIterations,
	})
}

// ---------------------------------------------------------------------------
// 4. Sub-agent fan-out + deterministic (non-LLM) merge (#4526)
// ---------------------------------------------------------------------------

// SubAgentStrategy is one parallel retrieval strategy for RunSubagentFanout
// — a distinct /rag/query parameterization, typically a SearchMode/
// EmbeddingSlot combination (#4514).
type SubAgentStrategy struct {
	// Name is a human-readable label for this strategy, surfaced on
	// SubAgentResult for tracing/logging — not sent over the wire.
	Name string
	// RagOptions is forwarded to RagClient.Query on top of whatever
	// RunSubagentFanout's own RagOptions already set. Non-zero fields here
	// override the base RagOptions field-by-field (see mergeRagOptions) —
	// e.g. {SearchMode: "lexical"}.
	RagOptions SmartRagOptions
}

// SubAgentResult is one strategy's outcome from RunSubagentFanout.
type SubAgentResult struct {
	Strategy SubAgentStrategy
	Response *RagQueryResponse
	// Confidence is this strategy's confidence score — its top hit's
	// RelevanceConfidence (#4520), or 0.0 when there are no hits or the
	// server predates #4520 (field unset).
	Confidence float64
}

// FanoutResult is the final result of RunSubagentFanout.
type FanoutResult struct {
	// Winner is the deterministically-chosen winner — strict argmax by
	// SubAgentResult.Confidence, ties broken by strategy declaration order.
	// Never chosen via an LLM call (#4526's AC #1).
	Winner SubAgentResult
	// MergedResponse is the winner's hits, merged (via RrfMerge) with any
	// non-winning survivor scoring at or above MergeThreshold. Equals
	// Winner.Response unchanged when no other survivor cleared
	// MergeThreshold.
	MergedResponse *RagQueryResponse
	// Included holds every SubAgentResult that cleared LowConfidenceFloor
	// (winner first, then any merged-in supporting evidence, in
	// descending confidence order).
	Included []SubAgentResult
	// Excluded holds every SubAgentResult discarded as noise — below
	// LowConfidenceFloor (or, in the all-strategies-below-floor fallback,
	// everything except the single best-scoring strategy).
	Excluded []SubAgentResult
}

func validateFanoutStrategies(strategies []SubAgentStrategy) error {
	if len(strategies) < MinFanoutStrategies || len(strategies) > MaxFanoutStrategies {
		return fmt.Errorf(
			"relata: RunSubagentFanout requires %d-%d strategies, got %d",
			MinFanoutStrategies, MaxFanoutStrategies, len(strategies),
		)
	}
	return nil
}

// subAgentStrategyConfidence computes the deterministic per-strategy
// confidence: the top hit's RelevanceConfidence (#4520), or 0.0 when there
// are no hits or the field is unset (server predates #4520). A pure
// function of the response already returned by /rag/query — no LLM/network
// call.
func subAgentStrategyConfidence(response *RagQueryResponse) float64 {
	if response == nil || len(response.Hits) == 0 {
		return 0.0
	}
	c := response.Hits[0].RelevanceConfidence
	if c == nil {
		return 0.0
	}
	return *c
}

// mergeRagOptions layers override's non-zero-value fields on top of base,
// approximating Python's dict-merge (`{**rag_kwargs, **strategy.rag_kwargs}`)
// with Go's zero-value-means-unset convention (the same simplification the
// file doc comment documents for RunAgenticLoopOptions.MaxIterations): a
// caller cannot use this to explicitly clear a base-level SearchMode/AsOf
// back to "" from a strategy, only to set a new one.
func mergeRagOptions(base, override SmartRagOptions) SmartRagOptions {
	out := base
	if override.TopK != nil {
		out.TopK = override.TopK
	}
	if override.Rerank {
		out.Rerank = true
	}
	if override.SearchMode != "" {
		out.SearchMode = override.SearchMode
	}
	if override.EmbeddingSlot != "" {
		out.EmbeddingSlot = override.EmbeddingSlot
	}
	if override.Filters != nil {
		out.Filters = override.Filters
	}
	if override.AsOf != "" {
		out.AsOf = override.AsOf
	}
	if override.ExpandWindow != nil {
		out.ExpandWindow = override.ExpandWindow
	}
	if override.GraphHops != 0 {
		out.GraphHops = override.GraphHops
	}
	return out
}

// mergeFanoutResults is the shared post-processing behind RunSubagentFanout:
// results is the (already-fetched) per-strategy outcome list; this function
// makes no I/O decisions and every branch is a pure function of its inputs
// — the deterministic winner-selection + merge invariant (#4526's AC #1)
// lives entirely here.
func mergeFanoutResults(results []SubAgentResult, mergeThreshold float64) FanoutResult {
	var survivors, excluded []SubAgentResult
	for _, r := range results {
		if r.Confidence >= LowConfidenceFloor {
			survivors = append(survivors, r)
		} else {
			excluded = append(excluded, r)
		}
	}
	if len(survivors) == 0 {
		// Every strategy scored below the noise floor — fall back to the
		// single highest-scoring result rather than returning nothing.
		fallbackIdx := 0
		for i, r := range results {
			if r.Confidence > results[fallbackIdx].Confidence {
				fallbackIdx = i
			}
		}
		survivors = []SubAgentResult{results[fallbackIdx]}
		excluded = nil
		for i, r := range results {
			if i != fallbackIdx {
				excluded = append(excluded, r)
			}
		}
	}

	// Deterministic winner: strict argmax by confidence — no LLM call. `>`
	// (not `>=`) keeps the earliest-declared strategy on a tie.
	winnerIdx := 0
	for i, r := range survivors {
		if r.Confidence > survivors[winnerIdx].Confidence {
			winnerIdx = i
		}
	}
	winner := survivors[winnerIdx]

	var supporting []SubAgentResult
	for i, r := range survivors {
		if i == winnerIdx {
			continue
		}
		if r.Confidence >= mergeThreshold {
			supporting = append(supporting, r)
		}
	}
	sort.SliceStable(supporting, func(i, j int) bool { return supporting[i].Confidence > supporting[j].Confidence })

	included := append([]SubAgentResult{winner}, supporting...)

	var mergedResponse *RagQueryResponse
	if len(supporting) > 0 {
		responses := make([]*RagQueryResponse, len(included))
		for i, r := range included {
			responses[i] = r.Response
		}
		k, err := RrfKForFanout(len(included))
		if err != nil {
			// len(included) >= 1 always here (winner is always present),
			// so RrfKForFanout's only error case (nSubqueries <= 0) is
			// unreachable — defend anyway rather than panic.
			k = 10.0
		}
		mergedResponse = RrfMerge(responses, k)
	} else {
		mergedResponse = winner.Response
	}

	return FanoutResult{Winner: winner, MergedResponse: mergedResponse, Included: included, Excluded: excluded}
}

// RunSubagentFanoutOptions configures RunSubagentFanout.
type RunSubagentFanoutOptions struct {
	Purpose string
	// MergeThreshold is the confidence floor for folding a non-winning
	// survivor in as supporting evidence; 0 uses MergeThreshold (the
	// verified default).
	MergeThreshold float64
	// RagOptions is forwarded to every strategy's /rag/query call before
	// that strategy's own SubAgentStrategy.RagOptions override on top (see
	// mergeRagOptions).
	RagOptions SmartRagOptions
}

// RunSubagentFanout runs strategies (2-5 parallel /rag/query
// parameterizations, concurrently via goroutines) and deterministically
// picks + merges a winner.
//
// Winner selection is a strict argmax over each strategy's confidence
// (subAgentStrategyConfidence) — **no LLM call anywhere in this function**,
// matching the reference platform system's threshold-merge design
// (explicitly no meta-LLM arbitration call).
func RunSubagentFanout(ctx context.Context, ragClient *RagClient, query, objectType string, strategies []SubAgentStrategy, opts RunSubagentFanoutOptions) (*FanoutResult, error) {
	if err := validateFanoutStrategies(strategies); err != nil {
		return nil, err
	}
	mergeThreshold := opts.MergeThreshold
	if mergeThreshold == 0 {
		mergeThreshold = MergeThreshold
	}

	type outcome struct {
		result SubAgentResult
		err    error
	}
	outcomes := make([]outcome, len(strategies))
	var wg sync.WaitGroup
	for i, strategy := range strategies {
		wg.Add(1)
		go func(i int, strategy SubAgentStrategy) {
			defer wg.Done()
			effectiveOptions := mergeRagOptions(opts.RagOptions, strategy.RagOptions)
			resp, err := ragClient.Query(ctx, ragOptionsToRequest(query, objectType, opts.Purpose, effectiveOptions))
			if err != nil {
				outcomes[i] = outcome{err: err}
				return
			}
			outcomes[i] = outcome{result: SubAgentResult{Strategy: strategy, Response: resp, Confidence: subAgentStrategyConfidence(resp)}}
		}(i, strategy)
	}
	wg.Wait()

	results := make([]SubAgentResult, len(strategies))
	for i, o := range outcomes {
		if o.err != nil {
			return nil, o.err
		}
		results[i] = o.result
	}

	merged := mergeFanoutResults(results, mergeThreshold)
	return &merged, nil
}
