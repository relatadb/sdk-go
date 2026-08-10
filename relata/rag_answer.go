package relata

import (
	"context"
	"fmt"
	"regexp"
)

// Package-level: the composed end-to-end RAG answer pipeline — the missing
// piece found during a post-epic alignment audit of #4529.
//
// Faithful Go port of sdks/python/relata/rag_answer.py (#4584, epic
// #4576) — closing the epic's own validation criterion ("an end-to-end
// query through the SDK loop produces a cited, faithfulness-checked answer
// with its reasoning trace recorded as governed rows"). This is the last
// file in the epic: it composes every other TS/Go-ported stage —
// rag_understanding.go (#4577), rag_loop.go (#4582), synthesis.go (#4579),
// rag_trace.go (#4583) — none of them reimplemented here.
//
// Design note — why this doesn't call SmartRagQuery directly. SmartRagQuery
// bundles its front-door gate (content-safety + SQL-shape routing) together
// with a single-pass retrieval call it makes itself. This file needs the
// *gate* but not that single retrieval pass — retrieval here is handled by
// RunAgenticLoop (iteration) or RunSubagentFanout (breadth), neither of
// which SmartRagQuery can delegate to. So this file calls the same gate
// primitives SmartRagQuery calls internally (CheckContentSafety,
// ClassifyQueryShape + the Route*Query functions) directly, once, up front
// — not a second, divergent gate implementation, the identical functions,
// just not wrapped in SmartRagQuery's own retrieval call.
//
// Design note — content-safety/SQL-routing runs once, not per loop
// iteration. A corrective-grading re-query is a HyDE-refined variant of the
// *same already-vetted query text* (see rag_loop.go's NeedsRequery
// handling), not new untrusted input — so gating it again per iteration
// would be redundant, not safer.
//
// Design note — fan-out and the loop are alternate retrieval strategies,
// not layered. When FanoutStrategies is given, RunSubagentFanout provides
// the one and only retrieval pass (breadth: several parallel strategies,
// deterministically merged) and the cost-ladder loop's iteration machinery
// does not run — its result is wrapped as a single-iteration LoopResult
// (StoppedReason="fanout_complete") purely so StoreReasoningTrace has the
// shape it expects; no iteration/re-query happens in this path. Leave
// FanoutStrategies nil for the loop's iteration/re-query behavior (depth)
// instead.
//
// Known limitation, not solved here. A Clarification (#4534, entity
// disambiguation) surfacing mid-loop is not specially handled — the loop's
// zero-hits heuristic (HeuristicGate scores empty hits 0.0 -> Retry) means
// it will retry to MaxIterations and give up, a bounded and honest failure
// rather than a silent wrong answer, but not a resolution. Resolving a
// clarification is a separate, multi-turn caller decision by design
// (#4534's own resume flow) — not something one call should auto-resolve.
//
// Structural adaptation from the Python source, required by language shape
// rather than a deliberate behavior change: Python exposes separate sync
// (run_rag_answer) and async (arun_rag_answer) twins because its
// RelataClient has two transports. Go has one calling convention (a
// context.Context-taking function that may block), so both collapse to the
// single RunRagAnswer here — the same collapse rag_loop.go,
// rag_understanding.go, and rag_trace.go already made. Like SmartRagQuery,
// RunRagAnswer takes a *RagClient and reaches into its unexported `c` field
// for SQL routing — same-package access, not a public back-reference.

// RagAnswerResult is the final result of RunRagAnswer.
//
// LoopResult/FanoutResult/SynthesisResult/Trace are nil when the query was
// refused or SQL-routed before any retrieval happened — check
// Response.Refused/Response.SQLResult (or Response.IsRefused()/
// Response.IsSQLRouted()) first.
type RagAnswerResult struct {
	Query           string
	Response        *SmartRagQueryResult
	LoopResult      *LoopResult
	FanoutResult    *FanoutResult
	SynthesisResult *SynthesisResult
	Trace           map[string]any
}

// RunRagAnswerOptions configures RunRagAnswer.
type RunRagAnswerOptions struct {
	Purpose string
	// EmbeddingFn is the caller-supplied embedding function for the
	// heuristic gate, forwarded to RunAgenticLoop. Required (matches
	// RunAgenticLoop's own required EmbeddingFn, even on the fan-out path
	// where it goes unused).
	EmbeddingFn EmbeddingFn
	// LLM is the text-completion callable used for the synthesis pass
	// (Synthesize's LLM). Required.
	LLM LlmFn
	// GraderFn is the caller-supplied batched corrective-retrieval grader,
	// forwarded to RunAgenticLoop.
	GraderFn GraderFn
	// HypothesisFn is the caller-supplied HyDE hypothesis generator,
	// forwarded to RunAgenticLoop.
	HypothesisFn HypothesisGenerator
	// WebSearchFallback is forwarded to RunAgenticLoop.
	WebSearchFallback WebSearchFallbackFn
	// MaxIterations is the hard cap on loop iterations; 0 uses
	// MaxIterations (the verified default). Forwarded to RunAgenticLoop.
	// Unused on the fan-out path.
	MaxIterations int
	// FanoutStrategies, when non-empty, replaces the cost-ladder loop with
	// RunSubagentFanout (breadth instead of depth) — see the file doc
	// comment's fan-out-vs-loop design note.
	FanoutStrategies []SubAgentStrategy
	// FaithfulnessCheckSet/FaithfulnessCheck mirror SynthesizeOptions' own
	// three-state-bool workaround — the zero value (false/false) still
	// means "run the check". Use RunRagAnswerOptions.DisableFaithfulnessCheck
	// to opt out explicitly.
	FaithfulnessCheck    bool
	FaithfulnessCheckSet bool
	// FaithfulnessLLM is forwarded to Synthesize.
	FaithfulnessLLM LlmFn
	// EntailmentFn is forwarded to Synthesize.
	EntailmentFn EntailmentFn
	// ContentSafetyPatterns is nil (the default) to disable the gate
	// entirely, or DangerousContentPatterns / a deployment's own mapping to
	// opt in (#4536).
	ContentSafetyPatterns map[string]*regexp.Regexp
	// StructuredFieldMap is the keyword -> canonical field name for #4535's
	// aggregation/negation/boolean/ranking SQL routing.
	StructuredFieldMap map[string]string
	// StructuredKnownFields restricts #4535's routing to fields actually
	// present on objectType's schema. nil = unrestricted.
	StructuredKnownFields []string
	// AttributeFieldMap overrides/extends AttributeFieldKeywords for
	// #4536's structured-attribute-filter routing.
	AttributeFieldMap map[string]string
	// AttributeKnownFields restricts attribute-filter routing to fields
	// actually present on objectType's schema. nil = unrestricted.
	AttributeKnownFields []string
	// CaseID is forwarded to StoreReasoningTrace.
	CaseID string
	// DurationMs is forwarded to StoreReasoningTrace. A pointer so an
	// explicit 0ms duration (a valid value) is distinguishable from "not
	// given" — same convention as BuildReasoningTraceOptions.DurationMs.
	DurationMs *int
	// RagOptions is forwarded to every /rag/query call made by
	// RunAgenticLoop or RunSubagentFanout.
	RagOptions SmartRagOptions
}

// DisableFaithfulnessCheck returns a copy of opts with the faithfulness
// pass turned off — mirrors SynthesizeOptions.DisableFaithfulnessCheck.
func (o RunRagAnswerOptions) DisableFaithfulnessCheck() RunRagAnswerOptions {
	o.FaithfulnessCheck = false
	o.FaithfulnessCheckSet = true
	return o
}

// dispatchSQLShape mirrors SmartRagQuery's SQL-shape dispatch switch
// exactly — same functions, same field-map contract, not a
// reimplementation.
func dispatchSQLShape(ctx context.Context, client *Client, query, objectType string, shape QueryShape, opts RunRagAnswerOptions) (*QueryResult, error) {
	routeOpts := RouteQueryOptions{Purpose: opts.Purpose}
	if shape == QueryShapeAttributeFilter {
		routeOpts.FieldMap = opts.AttributeFieldMap
		routeOpts.KnownFields = opts.AttributeKnownFields
	} else {
		routeOpts.FieldMap = opts.StructuredFieldMap
		routeOpts.KnownFields = opts.StructuredKnownFields
	}
	switch shape {
	case QueryShapeAttributeFilter:
		return RouteAttributeFilterQuery(ctx, client, query, objectType, routeOpts)
	case QueryShapeAggregation:
		return RouteAggregationQuery(ctx, client, query, objectType, routeOpts)
	case QueryShapeNegation:
		return RouteNegationQuery(ctx, client, query, objectType, routeOpts)
	case QueryShapeBoolean:
		return RouteBooleanQuery(ctx, client, query, objectType, routeOpts)
	default: // QueryShapeRanking
		return RouteRankingQuery(ctx, client, query, objectType, routeOpts)
	}
}

// fanoutAsLoopResult wraps a FanoutResult as a single-iteration LoopResult
// purely so StoreReasoningTrace (which requires a LoopResult) has the shape
// it expects. No iteration/re-query happened — this is bookkeeping, not a
// claim that the loop ran.
func fanoutAsLoopResult(fanoutResult *FanoutResult, query string) *LoopResult {
	confidence := fanoutResult.Winner.Confidence
	return &LoopResult{
		Response: fanoutResult.MergedResponse,
		Iterations: []LoopIteration{
			{Query: query, Response: fanoutResult.MergedResponse, Confidence: &confidence},
		},
		LLMCalls:      0,
		StoppedReason: "fanout_complete",
	}
}

// RunRagAnswer runs the full RAG-epic pipeline end to end: gate -> retrieve
// (iterate or fan out) -> synthesize + cite + faithfulness-check -> write
// the reasoning trace back as governed rows.
//
// Returns immediately after the gate, with LoopResult/FanoutResult/
// SynthesisResult/Trace all nil, when the query was refused
// (SmartRagQueryResult.Refused) or answered via SQL instead of retrieval
// (SmartRagQueryResult.SQLResult) — see the file doc comment for why gating
// happens once, not per iteration.
func RunRagAnswer(ctx context.Context, ragClient *RagClient, mcpClient *McpClient, query, objectType string, opts RunRagAnswerOptions) (*RagAnswerResult, error) {
	if refusal := CheckContentSafety(query, opts.ContentSafetyPatterns); refusal != nil {
		return &RagAnswerResult{
			Query:    query,
			Response: &SmartRagQueryResult{Hits: []RagHit{}, Refused: refusal},
		}, nil
	}

	shape := ClassifyQueryShape(query)
	client := ragClient.c
	if SQLRoutableShapes[shape] {
		sqlResult, err := dispatchSQLShape(ctx, client, query, objectType, shape, opts)
		if err != nil {
			return nil, err
		}
		if sqlResult != nil {
			return &RagAnswerResult{
				Query:    query,
				Response: &SmartRagQueryResult{Hits: []RagHit{}, SQLResult: sqlResult},
			}, nil
		}
		// Shape was SQL-routable but no predicate could be built (unknown
		// vocabulary/fields) — fall through to retrieval, flagged
		// low-confidence on the eventual response, exactly as
		// SmartRagQuery does.
	}

	var fanoutResult *FanoutResult
	var loopResult *LoopResult
	var err error
	if len(opts.FanoutStrategies) > 0 {
		fanoutResult, err = RunSubagentFanout(ctx, ragClient, query, objectType, opts.FanoutStrategies, RunSubagentFanoutOptions{
			Purpose:    opts.Purpose,
			RagOptions: opts.RagOptions,
		})
		if err != nil {
			return nil, err
		}
		loopResult = fanoutAsLoopResult(fanoutResult, query)
	} else {
		loopResult, err = RunAgenticLoop(ctx, ragClient, query, objectType, RunAgenticLoopOptions{
			Purpose:           opts.Purpose,
			EmbeddingFn:       opts.EmbeddingFn,
			GraderFn:          opts.GraderFn,
			HypothesisFn:      opts.HypothesisFn,
			WebSearchFallback: opts.WebSearchFallback,
			MaxIterations:     opts.MaxIterations,
			RagOptions:        opts.RagOptions,
		})
		if err != nil {
			return nil, err
		}
	}

	var response *SmartRagQueryResult
	if SQLRoutableShapes[shape] {
		response = &SmartRagQueryResult{
			Hits:          loopResult.Response.Hits,
			LowConfidence: true,
			LowConfidenceReason: fmt.Sprintf(
				"%s-shaped query could not be routed to SQL (no matching canonical field on %q); fell back to retrieval (#4536/#4535)",
				shape, objectType,
			),
		}
	} else {
		response = &SmartRagQueryResult{Hits: loopResult.Response.Hits}
	}

	synthesisResult, err := Synthesize(query, RagQueryResponse{Hits: response.Hits}, SynthesizeOptions{
		LLM:                  opts.LLM,
		FaithfulnessCheck:    opts.FaithfulnessCheck,
		FaithfulnessCheckSet: opts.FaithfulnessCheckSet,
		FaithfulnessLLM:      opts.FaithfulnessLLM,
		EntailmentFn:         opts.EntailmentFn,
	})
	if err != nil {
		return nil, err
	}

	trace, err := StoreReasoningTrace(ctx, mcpClient, query, loopResult, synthesisResult, &BuildReasoningTraceOptions{
		FanoutResult: fanoutResult,
		CaseID:       opts.CaseID,
		Purpose:      opts.Purpose,
		DurationMs:   opts.DurationMs,
	})
	if err != nil {
		return nil, err
	}

	return &RagAnswerResult{
		Query:           query,
		Response:        response,
		LoopResult:      loopResult,
		FanoutResult:    fanoutResult,
		SynthesisResult: synthesisResult,
		Trace:           trace,
	}, nil
}
