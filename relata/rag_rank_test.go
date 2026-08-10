package relata

import (
	"testing"
)

// Tests for MMR diversity selection (#4526), Go parity port of
// sdks/python/tests/test_rag_rank.py (#4578).
//
// Pure-function tests over RagHit — no client, no mocked transport needed.

func testHit(chunkID string, mutate func(*RagHit)) RagHit {
	h := RagHit{
		BM25Score:           1.0,
		VectorScore:         1.0,
		RerankScore:         nil,
		ChunkID:             chunkID,
		ReportID:            "doc-1",
		Text:                "text for " + chunkID,
		SectionPath:         []string{},
		PageStart:           1,
		PageEnd:             1,
		PrevChunkID:         nil,
		NextChunkID:         nil,
		EntityIDs:           []string{},
		RelevanceConfidence: nil,
	}
	if mutate != nil {
		mutate(&h)
	}
	return h
}

func floatPtr(v float64) *float64 { return &v }

// ── domain-configurable lambda (AC #3) ──────────────────────────────────────

func TestVerifiedLambdaDefaultsByDomain(t *testing.T) {
	cases := map[string]float64{
		"legal": 0.7, "clinical": 0.7, "medical": 0.7,
		"finance": 0.65, "research": 0.6, "general": 0.5, "code": 0.4,
	}
	for k, want := range cases {
		if got := MMRLambdaByPurpose[k]; got != want {
			t.Errorf("MMRLambdaByPurpose[%q] = %v, want %v", k, got, want)
		}
	}
	if DefaultMMRLambda != 0.5 {
		t.Errorf("DefaultMMRLambda = %v, want 0.5", DefaultMMRLambda)
	}
}

func TestMMRLambdaForPurpose_KnownDomains(t *testing.T) {
	cases := []struct {
		purpose string
		want    float64
	}{
		{"legal", 0.7}, {"Legal", 0.7}, {"CLINICAL", 0.7}, {"medical", 0.7},
		{"finance", 0.65}, {"research", 0.6}, {"general", 0.5}, {"code", 0.4},
	}
	for _, c := range cases {
		if got := MMRLambdaForPurpose(c.purpose); got != c.want {
			t.Errorf("MMRLambdaForPurpose(%q) = %v, want %v", c.purpose, got, c.want)
		}
	}
}

func TestMMRLambdaForPurpose_UnknownFallsBackToDefault(t *testing.T) {
	if got := MMRLambdaForPurpose("marketing"); got != DefaultMMRLambda {
		t.Errorf("got %v, want %v", got, DefaultMMRLambda)
	}
}

func TestMMRLambdaForPurpose_EmptyFallsBackToDefault(t *testing.T) {
	if got := MMRLambdaForPurpose(""); got != DefaultMMRLambda {
		t.Errorf("got %v, want %v", got, DefaultMMRLambda)
	}
}

// ── default relevance/similarity are pure functions over sub-scores ────────

func TestDefaultRelevance_PrefersRelevanceConfidenceWhenPresent(t *testing.T) {
	h := testHit("c1", func(h *RagHit) {
		h.RelevanceConfidence = floatPtr(0.42)
		h.BM25Score, h.VectorScore = 1.0, 1.0
	})
	if got := DefaultRelevance(h); got != 0.42 {
		t.Errorf("got %v, want 0.42", got)
	}
}

func TestDefaultRelevance_FallsBackToScoreAverage(t *testing.T) {
	h := testHit("c1", func(h *RagHit) {
		h.RelevanceConfidence = nil
		h.BM25Score, h.VectorScore = 0.8, 0.4
	})
	if got := DefaultRelevance(h); got < 0.6-1e-9 || got > 0.6+1e-9 {
		t.Errorf("got %v, want ~0.6", got)
	}
}

func TestDefaultTextSimilarity_IdenticalTextIsOne(t *testing.T) {
	a := testHit("a", func(h *RagHit) { h.Text = "quarterly revenue rose sharply" })
	b := testHit("b", func(h *RagHit) { h.Text = "quarterly revenue rose sharply" })
	if got := DefaultTextSimilarity(a, b); got != 1.0 {
		t.Errorf("got %v, want 1.0", got)
	}
}

func TestDefaultTextSimilarity_DisjointTextIsZero(t *testing.T) {
	a := testHit("a", func(h *RagHit) { h.Text = "quarterly revenue" })
	b := testHit("b", func(h *RagHit) { h.Text = "unrelated topic entirely" })
	if got := DefaultTextSimilarity(a, b); got != 0.0 {
		t.Errorf("got %v, want 0.0", got)
	}
}

func TestDefaultTextSimilarity_PartialOverlap(t *testing.T) {
	a := testHit("a", func(h *RagHit) { h.Text = "alpha beta gamma" })
	b := testHit("b", func(h *RagHit) { h.Text = "alpha beta delta" })
	// tokens: {alpha,beta,gamma} vs {alpha,beta,delta} -> intersection 2, union 4
	if got := DefaultTextSimilarity(a, b); got != 0.5 {
		t.Errorf("got %v, want 0.5", got)
	}
}

// ── MMRSelect — pure, deterministic, no LLM/embedding/network call ────────

func chunkIDs(hits []RagHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ChunkID
	}
	return out
}

func assertChunkIDs(t *testing.T, got []RagHit, want []string) {
	t.Helper()
	gotIDs := chunkIDs(got)
	if len(gotIDs) != len(want) {
		t.Fatalf("got %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("got %v, want %v", gotIDs, want)
		}
	}
}

func TestMMRSelect_Lambda1IsPureRelevanceRanking(t *testing.T) {
	hits := []RagHit{
		testHit("low", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.2); h.Text = "alpha" }),
		testHit("high", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.9); h.Text = "alpha" }),
		testHit("mid", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.5); h.Text = "alpha" }),
	}
	selected, err := MMRSelect(hits, &MMRSelectOptions{LambdaMult: 1.0, LambdaMultSet: true})
	if err != nil {
		t.Fatal(err)
	}
	assertChunkIDs(t, selected, []string{"high", "mid", "low"})
}

func TestMMRSelect_Lambda0MaximizesDiversityOnly(t *testing.T) {
	hits := []RagHit{
		testHit("first", func(h *RagHit) {
			h.RelevanceConfidence = floatPtr(0.9)
			h.Text = "alpha beta gamma"
		}),
		testHit("dup", func(h *RagHit) {
			h.RelevanceConfidence = floatPtr(0.9)
			h.Text = "alpha beta gamma"
		}),
		testHit("diverse", func(h *RagHit) {
			h.RelevanceConfidence = floatPtr(0.9)
			h.Text = "totally unrelated content"
		}),
	}
	selected, err := MMRSelect(hits, &MMRSelectOptions{
		LambdaMult: 0.0, LambdaMultSet: true, TopN: 2, TopNSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertChunkIDs(t, selected, []string{"first", "diverse"})
}

func TestMMRSelect_RespectsTopN(t *testing.T) {
	hits := make([]RagHit, 5)
	for i := range hits {
		i := i
		hits[i] = testHit(
			string(rune('0'+i)),
			func(h *RagHit) { h.RelevanceConfidence = floatPtr(1.0 - float64(i)*0.1) },
		)
	}
	// re-set chunk IDs explicitly (rune-based IDs above were just placeholders)
	for i := range hits {
		hits[i].ChunkID = "c" + string(rune('0'+i))
	}
	selected, err := MMRSelect(hits, &MMRSelectOptions{LambdaMult: 1.0, LambdaMultSet: true, TopN: 2, TopNSet: true})
	if err != nil {
		t.Fatal(err)
	}
	assertChunkIDs(t, selected, []string{"c0", "c1"})
}

func TestMMRSelect_TopNLargerThanPoolReturnsEverything(t *testing.T) {
	hits := []RagHit{testHit("a", nil), testHit("b", nil)}
	selected, err := MMRSelect(hits, &MMRSelectOptions{TopN: 10, TopNSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 {
		t.Errorf("got %d hits, want 2", len(selected))
	}
}

func TestMMRSelect_EmptyInputReturnsEmpty(t *testing.T) {
	selected, err := MMRSelect(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 0 {
		t.Errorf("got %d hits, want 0", len(selected))
	}
}

func TestMMRSelect_RejectsOutOfRangeLambda(t *testing.T) {
	if _, err := MMRSelect([]RagHit{testHit("a", nil)}, &MMRSelectOptions{LambdaMult: 1.5, LambdaMultSet: true}); err == nil {
		t.Error("expected error for lambdaMult=1.5")
	}
	if _, err := MMRSelect([]RagHit{testHit("a", nil)}, &MMRSelectOptions{LambdaMult: -0.1, LambdaMultSet: true}); err == nil {
		t.Error("expected error for lambdaMult=-0.1")
	}
}

func TestMMRSelect_IsDeterministicAcrossRepeatedCalls(t *testing.T) {
	hits := []RagHit{
		testHit("a", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.8); h.Text = "one two three" }),
		testHit("b", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.7); h.Text = "two three four" }),
		testHit("c", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.6); h.Text = "five six seven" }),
	}
	opts := &MMRSelectOptions{LambdaMult: 0.5, LambdaMultSet: true}
	first, err := MMRSelect(hits, opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MMRSelect(hits, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertChunkIDs(t, second, chunkIDs(first))
}

func TestMMRSelect_AcceptsCustomRelevanceAndSimilarityFns(t *testing.T) {
	hits := []RagHit{testHit("a", nil), testHit("b", nil), testHit("c", nil)}
	calls := map[string]bool{}
	relevanceByID := map[string]float64{"a": 0.1, "b": 0.9, "c": 0.5}
	relevanceFn := func(h RagHit) float64 {
		calls[h.ChunkID] = true
		return relevanceByID[h.ChunkID]
	}
	similarityFn := func(x, y RagHit) float64 { return 0.0 }

	selected, err := MMRSelect(hits, &MMRSelectOptions{
		LambdaMult: 1.0, LambdaMultSet: true,
		RelevanceFn: relevanceFn, SimilarityFn: similarityFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertChunkIDs(t, selected, []string{"b", "c", "a"})
	for _, id := range []string{"a", "b", "c"} {
		if !calls[id] {
			t.Errorf("relevanceFn never called for %q", id)
		}
	}
}

// ── MMRSelectForPurpose — the purpose-keyed convenience entry point ─────

func TestMMRSelectForPurpose_UsesDomainLambda(t *testing.T) {
	hits := []RagHit{
		testHit("low", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.2); h.Text = "alpha" }),
		testHit("high", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.9); h.Text = "alpha" }),
	}
	selected, err := MMRSelectForPurpose(hits, "legal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected[0].ChunkID != "high" {
		t.Errorf("got %q, want %q", selected[0].ChunkID, "high")
	}
}

func TestMMRSelectForPurpose_EmptyUsesGeneralDefault(t *testing.T) {
	hits := []RagHit{
		testHit("a", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.9) }),
		testHit("b", func(h *RagHit) { h.RelevanceConfidence = floatPtr(0.1) }),
	}
	selected, err := MMRSelectForPurpose(hits, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected[0].ChunkID != "a" {
		t.Errorf("got %q, want %q", selected[0].ChunkID, "a")
	}
}
