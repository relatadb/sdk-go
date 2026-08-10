package relata

import (
	"strings"
	"testing"
)

// Tests for citation injection + post-synthesis faithfulness scoring — RAG
// epic highest-priority SDK ticket (#4527, ported from
// sdks/python/tests/test_synthesis.py, #4579). Every LLM call in these
// tests is a plain Go stub — RelataDB has no server-side agent loop
// (ADR-013) and this module is intentionally provider-agnostic.

func hitSupported() RagHit {
	next := "chunk-2"
	return RagHit{
		BM25Score:   4.2,
		VectorScore: 0.83,
		ChunkID:     "chunk-1",
		ReportID:    "doc-1",
		Text:        "RelataDB fuses BM25 and vector retrieval natively via RRF.",
		SectionPath: []string{"3", "3.2"},
		PageStart:   5,
		PageEnd:     6,
		NextChunkID: &next,
		EntityIDs:   []string{"ent-1"},
	}
}

func hitOther() RagHit {
	prev := "chunk-1"
	return RagHit{
		BM25Score:   3.1,
		VectorScore: 0.61,
		ChunkID:     "chunk-2",
		ReportID:    "doc-1",
		Text:        "The fused ranking uses reciprocal rank fusion (RRF) across channels.",
		SectionPath: []string{"3", "3.3"},
		PageStart:   6,
		PageEnd:     7,
		PrevChunkID: &prev,
		EntityIDs:   []string{},
	}
}

func responseWithHits(hits ...RagHit) RagQueryResponse {
	return RagQueryResponse{Hits: hits}
}

func llmFn(text string) LlmFn {
	return func(string) (string, error) { return text, nil }
}

func alwaysSupported(string, []string) (bool, error) { return true, nil }
func neverSupported(string, []string) (bool, error)  { return false, nil }

// ── BuildSynthesisPrompt — inline citation instructions ────────────────────

func TestBuildSynthesisPrompt_IncludesCitationGradeFieldsForEveryHit(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	prompt := BuildSynthesisPrompt("How does RelataDB rank hybrid results?", resp.Hits)
	for _, want := range []string{
		"[chunk-1]", "[chunk-2]", "3 > 3.2", "p.5-6",
		"RelataDB fuses BM25 and vector retrieval natively via RRF.",
		"Never invent a chunk id",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// ── Synthesize — inline citation injection, no fabrication possible ────────

func TestSynthesize_ResolvesRealCitationsInline(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	result, err := Synthesize("How does RelataDB rank hybrid results?", resp, SynthesizeOptions{
		LLM:          llmFn("RelataDB fuses BM25 and vector retrieval natively [chunk-1]."),
		EntailmentFn: alwaysSupported,
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(result.Citations) != 1 {
		t.Fatalf("citations = %d, want 1", len(result.Citations))
	}
	c := result.Citations[0]
	if c.ChunkID != "chunk-1" || c.ReportID != "doc-1" {
		t.Fatalf("citation = %+v", c)
	}
	if len(c.SectionPath) != 2 || c.SectionPath[0] != "3" || c.SectionPath[1] != "3.2" {
		t.Fatalf("section_path = %v", c.SectionPath)
	}
	if c.PageStart != 5 || c.PageEnd != 6 {
		t.Fatalf("pages = %d-%d", c.PageStart, c.PageEnd)
	}
	if !strings.Contains(result.Answer, "[chunk-1]") {
		t.Fatalf("answer missing citation marker: %q", result.Answer)
	}
	if result.Sentences[0].Citations[0].ChunkID != "chunk-1" {
		t.Fatalf("sentence citation = %+v", result.Sentences[0].Citations)
	}
}

func TestSynthesize_StripsFabricatedCitationByConstruction(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	result, err := Synthesize("q", resp, SynthesizeOptions{
		LLM:          llmFn("RelataDB invented this fact [chunk-does-not-exist]."),
		EntailmentFn: alwaysSupported,
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(result.Citations) != 0 {
		t.Fatalf("citations = %v, want none", result.Citations)
	}
	if strings.Contains(result.Answer, "chunk-does-not-exist") {
		t.Fatalf("answer leaked fabricated marker: %q", result.Answer)
	}
	if len(result.Sentences[0].Citations) != 0 {
		t.Fatalf("sentence citations = %v, want none", result.Sentences[0].Citations)
	}
}

func TestSynthesize_DedupesCitationsAcrossSentences(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	result, err := Synthesize("q", resp, SynthesizeOptions{
		LLM: llmFn(
			"RelataDB fuses BM25 and vector retrieval natively [chunk-1]. " +
				"It uses RRF to combine channel scores [chunk-1].",
		),
		EntailmentFn: alwaysSupported,
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(result.Citations) != 1 || result.Citations[0].ChunkID != "chunk-1" {
		t.Fatalf("citations = %v, want exactly one chunk-1", result.Citations)
	}
	if len(result.Sentences) != 2 {
		t.Fatalf("sentences = %d, want 2", len(result.Sentences))
	}
}

// ── faithfulness pass — on by default, marks unsupported claims ────────────

func TestSynthesize_FaithfulnessCheckRunsByDefault(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	calls := 0
	_, err := Synthesize("q", resp, SynthesizeOptions{
		LLM: llmFn("RelataDB fuses BM25 and vector retrieval natively [chunk-1]."),
		EntailmentFn: func(string, []string) (bool, error) {
			calls++
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if calls != 1 {
		t.Fatalf("entailment calls = %d, want 1 (faithfulness check must run by default)", calls)
	}
}

func TestSynthesize_MarksDeliberatelyInjectedUnsupportedClaim(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	result, err := Synthesize("q", resp, SynthesizeOptions{
		LLM: llmFn(
			"RelataDB fuses BM25 and vector retrieval natively [chunk-1]. " +
				"RelataDB was founded on the moon in 1969 [chunk-2].",
		),
		EntailmentFn: func(sentence string, _ []string) (bool, error) {
			// Only the fabricated "founded on the moon" claim fails
			// entailment against its cited evidence — deliberately
			// injected to prove faithfulness marking actually happens.
			return !strings.Contains(strings.ToLower(sentence), "moon"), nil
		},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if result.UnsupportedCount != 1 {
		t.Fatalf("unsupported_count = %d, want 1", result.UnsupportedCount)
	}
	if !result.HasUnsupportedClaims() {
		t.Fatal("HasUnsupportedClaims() = false, want true")
	}
	if len(result.Sentences) != 2 {
		t.Fatalf("sentences = %d, want 2", len(result.Sentences))
	}
	supported, unsupported := result.Sentences[0], result.Sentences[1]
	if !supported.Supported {
		t.Fatal("first sentence should be supported")
	}
	if strings.Contains(supported.Text, "[unsupported]") {
		t.Fatalf("supported sentence marked: %q", supported.Text)
	}
	if unsupported.Supported {
		t.Fatal("second sentence should be unsupported")
	}
	if !strings.HasSuffix(unsupported.Text, "[unsupported]") {
		t.Fatalf("unsupported sentence not marked: %q", unsupported.Text)
	}
	if !strings.Contains(result.Answer, "[unsupported]") {
		t.Fatalf("answer missing unsupported marker: %q", result.Answer)
	}
	if len(unsupported.Citations) == 0 || unsupported.Citations[0].ChunkID != "chunk-2" {
		t.Fatalf("unsupported sentence citations = %v", unsupported.Citations)
	}
}

func TestSynthesize_CustomUnsupportedMarker(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	result, err := Synthesize("q", resp, SynthesizeOptions{
		LLM:               llmFn("A totally fabricated claim with no citation at all."),
		EntailmentFn:      neverSupported,
		UnsupportedMarker: "[NEEDS VERIFICATION]",
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if !strings.HasSuffix(result.Answer, "[NEEDS VERIFICATION]") {
		t.Fatalf("answer = %q, want custom marker suffix", result.Answer)
	}
}

func TestSynthesize_FaithfulnessCheckCanBeDisabled(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	opts := SynthesizeOptions{
		LLM: llmFn("Some claim [chunk-1]."),
		EntailmentFn: func(string, []string) (bool, error) {
			t.Fatal("entailment_fn must not be called when faithfulness check is disabled")
			return false, nil
		},
	}.DisableFaithfulnessCheck()
	result, err := Synthesize("q", resp, opts)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if result.UnsupportedCount != 0 {
		t.Fatalf("unsupported_count = %d, want 0", result.UnsupportedCount)
	}
	for _, s := range result.Sentences {
		if !s.Supported {
			t.Fatalf("sentence marked unsupported with faithfulness check disabled: %+v", s)
		}
	}
}

func TestSynthesize_DefaultEntailmentFnMakesSecondIndependentLlmCall(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	var prompts []string
	llm := func(prompt string) (string, error) {
		prompts = append(prompts, prompt)
		if strings.Contains(prompt, "Claim:") {
			return "NO", nil
		}
		return "RelataDB fuses BM25 and vector retrieval natively [chunk-1].", nil
	}
	result, err := Synthesize("q", resp, SynthesizeOptions{LLM: llm})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(prompts) != 2 {
		t.Fatalf("llm calls = %d, want 2 (one synthesis, one entailment)", len(prompts))
	}
	if !strings.Contains(prompts[1], "Claim:") || !strings.Contains(prompts[1], "Evidence:") {
		t.Fatalf("second prompt missing entailment framing: %q", prompts[1])
	}
	if result.UnsupportedCount != 1 {
		t.Fatalf("unsupported_count = %d, want 1", result.UnsupportedCount)
	}
}

func TestSynthesize_UncitedSentenceWithNoEvidenceIsUnsupportedByDefault(t *testing.T) {
	resp := responseWithHits(hitSupported(), hitOther())
	result, err := Synthesize("q", resp, SynthesizeOptions{
		LLM: llmFn("This sentence cites nothing."),
		EntailmentFn: func(_ string, evidence []string) (bool, error) {
			// No citation resolved -> evidence falls back to every
			// retrieved hit's text (non-empty here), so this exercises the
			// "has some evidence but still fails" path rather than the
			// empty-evidence short-circuit covered by the "second
			// independent LLM call" test's "NO" case.
			return len(evidence) == 0, nil
		},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if result.UnsupportedCount != 1 {
		t.Fatalf("unsupported_count = %d, want 1", result.UnsupportedCount)
	}
}

func TestSynthesize_WithNoHitsProducesNoCitations(t *testing.T) {
	resp := responseWithHits()
	result, err := Synthesize("q", resp, SynthesizeOptions{
		LLM: llmFn("I don't have enough information to answer that."),
		EntailmentFn: func(_ string, evidence []string) (bool, error) {
			return len(evidence) == 0, nil
		},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(result.Citations) != 0 {
		t.Fatalf("citations = %v, want none", result.Citations)
	}
}

func TestSynthesize_RequiresLLM(t *testing.T) {
	resp := responseWithHits(hitSupported())
	_, err := Synthesize("q", resp, SynthesizeOptions{})
	if err == nil {
		t.Fatal("Synthesize with no LLM should error")
	}
}
