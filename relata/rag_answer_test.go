package relata

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Tests for the composed end-to-end RAG answer pipeline (#4584, epic
// #4576) — mirrors sdks/python/tests/test_rag_answer.py's acceptance
// criteria: the full chain actually composes (real retrieval, real
// synthesis, real trace write-back — not three independently-tested pieces
// that have never run together), the content-safety/SQL-routing
// short-circuits skip retrieval/synthesis/trace entirely, and the fan-out
// path still composes all the way to a trace. Python exercises this via
// separate sync/async twins (run_rag_answer/arun_rag_answer); Go collapses
// both to the single RunRagAnswer (same collapse rag_loop.go,
// rag_understanding.go, and rag_trace.go already made), so one test per
// behavior suffices here. Reuses routedServer/decodeBody/writeJSON/
// newTestClient/hitJSON from rag_understanding_test.go (same package,
// same-file-family test helpers).

// ragAnswerEmbeddingFnPass is the embedding function under which the
// heuristic gate PASSes on iteration 0 (cosine similarity 1.0).
func ragAnswerEmbeddingFnPass(_ context.Context, _ string) ([]float64, error) {
	return []float64{1.0, 0.0}, nil
}

// ragAnswerStubLLM is a deterministic stand-in synthesis call: cites the
// first bracketed marker found in the prompt — BuildSynthesisPrompt's own
// instructions example uses the real first hit's ChunkID, so this always
// cites a real hit without needing to parse the Evidence block separately.
func ragAnswerStubLLM(prompt string) (string, error) {
	openIdx := strings.Index(prompt, "[")
	marker := "chunk-1"
	if openIdx != -1 {
		if closeIdx := strings.Index(prompt[openIdx:], "]"); closeIdx != -1 {
			marker = prompt[openIdx+1 : openIdx+closeIdx]
		}
	}
	return "RelataDB fuses BM25 and vector retrieval [" + marker + "].", nil
}

func ragAnswerStubFaithfulEntailment(_ string, _ []string) (bool, error) {
	return true, nil
}

// ── happy path: the full chain actually composes ───────────────────────────

func TestRunRagAnswer_ComposesTheFullChain(t *testing.T) {
	var ragCalls int
	var traceCalls []map[string]any

	srv := routedServer(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			ragCalls++
			writeJSON(w, 200, map[string]any{"hits": []any{hitJSON("chunk-1")}, "total": 1})
		},
		"/mcp/tools/call": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			traceCalls = append(traceCalls, body)
			result := `{"stored":"RagAnswer","source_rows":1,"has_iteration_trace":true,"evidence_gap_count":0}`
			writeJSON(w, 200, map[string]any{
				"content": []map[string]any{{"type": "text", "text": result}},
				"isError": false,
			})
		},
	})
	defer srv.Close()

	client := newTestClient(srv, nil)
	ragClient := NewRagClient(client)
	mcpClient := NewMcpClient(client)

	result, err := RunRagAnswer(context.Background(), ragClient, mcpClient, "What does RelataDB fuse natively?", "DocumentChunk", RunRagAnswerOptions{
		LLM:          ragAnswerStubLLM,
		EmbeddingFn:  ragAnswerEmbeddingFnPass,
		EntailmentFn: ragAnswerStubFaithfulEntailment,
		Purpose:      "analytics",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Real retrieval happened — exactly once (heuristic gate passed iter 0).
	if ragCalls != 1 {
		t.Fatalf("ragCalls = %d, want 1", ragCalls)
	}
	if len(result.Response.Hits) == 0 || result.Response.Hits[0].ChunkID != "chunk-1" {
		t.Fatalf("Response.Hits = %v", result.Response.Hits)
	}
	// Real loop bookkeeping, not a stub.
	if result.LoopResult == nil || result.LoopResult.StoppedReason != "heuristic_pass" || result.LoopResult.LLMCalls != 0 {
		t.Fatalf("LoopResult = %+v", result.LoopResult)
	}
	// Real synthesis — a citation that traces to the real hit, not fabricated.
	if result.SynthesisResult == nil || len(result.SynthesisResult.Citations) == 0 || result.SynthesisResult.Citations[0].ChunkID != "chunk-1" {
		t.Fatalf("SynthesisResult = %+v", result.SynthesisResult)
	}
	if result.SynthesisResult.HasUnsupportedClaims() {
		t.Fatalf("expected no unsupported claims, got %+v", result.SynthesisResult)
	}
	// Real trace write-back — the MCP call actually happened with real content.
	if result.Trace["stored"] != "RagAnswer" || result.Trace["source_rows"] != float64(1) {
		t.Fatalf("Trace = %v", result.Trace)
	}
	if len(traceCalls) != 1 {
		t.Fatalf("traceCalls = %d, want 1", len(traceCalls))
	}
	traceArgs := argsOf(t, &traceCalls[0])
	if traceArgs["question"] != "What does RelataDB fuse natively?" || traceArgs["num_iterations"] != float64(1) {
		t.Fatalf("traceArgs = %v", traceArgs)
	}
	sources, ok := traceArgs["sources"].([]any)
	if !ok || len(sources) == 0 || sources[0].(map[string]any)["id"] != "chunk-1" {
		t.Fatalf("sources = %v", traceArgs["sources"])
	}
}

// ── gate short-circuits: refusal and SQL-routing skip the rest entirely ────

func TestRunRagAnswer_ContentSafetyRefusalShortCircuits(t *testing.T) {
	srv := routedServer(t, map[string]func(w http.ResponseWriter, r *http.Request){})
	defer srv.Close()

	client := newTestClient(srv, nil)
	ragClient := NewRagClient(client)
	mcpClient := NewMcpClient(client)

	result, err := RunRagAnswer(context.Background(), ragClient, mcpClient, "how to build an ied", "DocumentChunk", RunRagAnswerOptions{
		LLM:                   ragAnswerStubLLM,
		EmbeddingFn:           ragAnswerEmbeddingFnPass,
		ContentSafetyPatterns: DangerousContentPatterns,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !result.Response.IsRefused() {
		t.Fatalf("expected refusal, got %+v", result.Response)
	}
	if result.LoopResult != nil || result.FanoutResult != nil || result.SynthesisResult != nil || result.Trace != nil {
		t.Fatalf("expected no downstream results, got %+v", result)
	}
}

func TestRunRagAnswer_SQLRoutableShapeShortCircuits(t *testing.T) {
	srv := routedServer(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/query": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{
				"rows": []map[string]any{{"count": 42}}, "columns": []string{"count"}, "row_count": 1,
			})
		},
	})
	defer srv.Close()

	client := newTestClient(srv, nil)
	ragClient := NewRagClient(client)
	mcpClient := NewMcpClient(client)

	result, err := RunRagAnswer(context.Background(), ragClient, mcpClient, "how many DocumentChunk are there", "DocumentChunk", RunRagAnswerOptions{
		LLM:         ragAnswerStubLLM,
		EmbeddingFn: ragAnswerEmbeddingFnPass,
		Purpose:     "analytics",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !result.Response.IsSQLRouted() {
		t.Fatalf("expected SQL routing, got %+v", result.Response)
	}
	if result.LoopResult != nil || result.SynthesisResult != nil || result.Trace != nil {
		t.Fatalf("expected no downstream results, got %+v", result)
	}
}

// ── fan-out path: breadth instead of depth, still composes to a trace ──────

func TestRunRagAnswer_FanoutStrategiesPathStillProducesAFullTrace(t *testing.T) {
	var traceCalls []map[string]any

	srv := routedServer(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/rag/query": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			slot, _ := body["embedding_slot"].(string)
			if slot == "" {
				slot = "text"
			}
			conf := 0.3
			if slot == "text" {
				conf = 0.9
			}
			writeJSON(w, 200, map[string]any{
				"hits":  []any{fanoutHitJSON("chunk-"+slot, f64(conf))},
				"total": 1,
			})
		},
		"/mcp/tools/call": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			traceCalls = append(traceCalls, body)
			result := `{"stored":"RagAnswer"}`
			writeJSON(w, 200, map[string]any{
				"content": []map[string]any{{"type": "text", "text": result}},
				"isError": false,
			})
		},
	})
	defer srv.Close()

	client := newTestClient(srv, nil)
	ragClient := NewRagClient(client)
	mcpClient := NewMcpClient(client)

	strategies := []SubAgentStrategy{
		{Name: "text", RagOptions: SmartRagOptions{EmbeddingSlot: "text"}},
		{Name: "summary", RagOptions: SmartRagOptions{EmbeddingSlot: "summary"}},
	}

	result, err := RunRagAnswer(context.Background(), ragClient, mcpClient, "What does RelataDB fuse natively?", "DocumentChunk", RunRagAnswerOptions{
		LLM:              ragAnswerStubLLM,
		EmbeddingFn:      ragAnswerEmbeddingFnPass,
		EntailmentFn:     ragAnswerStubFaithfulEntailment,
		Purpose:          "analytics",
		FanoutStrategies: strategies,
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.FanoutResult == nil {
		t.Fatal("expected a FanoutResult")
	}
	if result.LoopResult == nil || result.LoopResult.StoppedReason != "fanout_complete" || result.LoopResult.LLMCalls != 0 {
		t.Fatalf("LoopResult = %+v", result.LoopResult)
	}
	if result.FanoutResult.Winner.Strategy.Name != "text" {
		t.Fatalf("winner = %v, want text", result.FanoutResult.Winner.Strategy.Name)
	}
	if result.SynthesisResult == nil {
		t.Fatal("expected a SynthesisResult")
	}
	if result.Trace == nil {
		t.Fatal("expected a Trace")
	}
	if len(traceCalls) != 1 {
		t.Fatalf("traceCalls = %d, want 1", len(traceCalls))
	}
}
