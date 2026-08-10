package relata

import (
	"fmt"
	"regexp"
	"strings"
)

// MMR (Maximal Marginal Relevance) diversity selection — RAG epic (#4526),
// Go parity port of sdks/python/relata/rag_rank.py (#4578).
//
// Greedy selection maximizing
//
//	lambdaMult*relevance - (1-lambdaMult)*maxSimilarityToSelected
//
// over the candidate set, verified live in the reference retrieval toolkit
// studied for ADR-0299. Both terms are pure functions of what
// POST /rag/query already returned — RelataDB's wire contract carries
// per-channel scores (BM25Score/VectorScore, #4514) and the non-LLM
// RelevanceConfidence signal (#4520), never raw embedding vectors, so
// MMRSelect needs no embedding call, no LLM call, and no network I/O: it is
// deterministic given its inputs.
//
//   - Relevance (DefaultRelevance) defaults to a hit's RelevanceConfidence
//     when the server provides it, falling back to the plain average of
//     BM25Score/VectorScore for a server that predates #4520.
//   - Similarity (DefaultTextSimilarity) defaults to Jaccard token-set
//     overlap over each hit's Text — the one per-hit field that can stand in
//     for "how similar are these two chunks" without a vector. Callers with
//     a real embedding function available can pass a smarter SimilarityFn
//     instead; MMRSelect takes both as plain functions so neither default is
//     load-bearing.
//
// Lambda is domain-configurable, not a single global constant (#4526's AC
// #3): MMRLambdaByPurpose keys the verified reference-toolkit defaults by
// RelataDB's existing purpose governance dimension via MMRLambdaForPurpose.

// MMRLambdaByPurpose is the purpose-keyed MMR lambda defaults
// (case-insensitive lookup via MMRLambdaForPurpose). Higher lambda weights
// relevance over diversity — legal/clinical/medical domains want the
// most-relevant chunks even if similar; code retrieval wants the most
// diverse set even at some relevance cost.
var MMRLambdaByPurpose = map[string]float64{
	"legal":    0.7,
	"clinical": 0.7,
	"medical":  0.7,
	"finance":  0.65,
	"research": 0.6,
	"general":  0.5,
	"code":     0.4,
}

// DefaultMMRLambda is the fallback lambda for a purpose not present in
// MMRLambdaByPurpose (including empty) — the "general" value, an even
// relevance/diversity split.
var DefaultMMRLambda = MMRLambdaByPurpose["general"]

// MMRLambdaForPurpose returns the verified MMR lambda for purpose
// (case-insensitive), or DefaultMMRLambda when purpose is empty or not one
// of MMRLambdaByPurpose's keys.
func MMRLambdaForPurpose(purpose string) float64 {
	if purpose == "" {
		return DefaultMMRLambda
	}
	if v, ok := MMRLambdaByPurpose[strings.ToLower(purpose)]; ok {
		return v
	}
	return DefaultMMRLambda
}

// RelevanceFn is the signature for MMR's relevance term: one score per
// candidate hit.
type RelevanceFn func(hit RagHit) float64

// SimilarityFn is the signature for MMR's diversity term: similarity
// between two hits.
type SimilarityFn func(a, b RagHit) float64

var tokenRe = regexp.MustCompile(`[a-z0-9]+`)

// DefaultRelevance is the pure relevance term: hit.RelevanceConfidence
// (#4520) when present, otherwise the average of
// hit.BM25Score/hit.VectorScore (#4514) — always a function of sub-scores
// already on the hit, never a new call.
func DefaultRelevance(hit RagHit) float64 {
	if hit.RelevanceConfidence != nil {
		return *hit.RelevanceConfidence
	}
	return (hit.BM25Score + hit.VectorScore) / 2.0
}

func tokenSet(text string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, tok := range tokenRe.FindAllString(strings.ToLower(text), -1) {
		out[tok] = struct{}{}
	}
	return out
}

// DefaultTextSimilarity is the pure diversity term: Jaccard token-set
// overlap of a.Text/b.Text. 0.0 when either hit's Text has no tokens.
func DefaultTextSimilarity(a, b RagHit) float64 {
	tokensA, tokensB := tokenSet(a.Text), tokenSet(b.Text)
	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0.0
	}
	intersection := 0
	for t := range tokensA {
		if _, ok := tokensB[t]; ok {
			intersection++
		}
	}
	union := len(tokensA) + len(tokensB) - intersection
	return float64(intersection) / float64(union)
}

// MMRSelectOptions configures MMRSelect. A nil *MMRSelectOptions (or a
// zero-value one with no fields explicitly set) selects with
// DefaultMMRLambda over every hit in the input, using
// DefaultRelevance/DefaultTextSimilarity — matching the Python reference's
// keyword-argument defaults.
type MMRSelectOptions struct {
	// LambdaMult is the relevance/diversity trade-off in [0.0, 1.0].
	// Ignored unless LambdaMultSet is true (0.0 is itself a valid,
	// meaningful setting — pure diversity — so it cannot double as "unset").
	LambdaMult float64
	// LambdaMultSet reports whether LambdaMult should override
	// DefaultMMRLambda. See MMRSelectForPurpose for the purpose-keyed
	// convenience entry point that sets this via MMRLambdaForPurpose.
	LambdaMultSet bool
	// TopN caps the number of hits returned. Ignored unless TopNSet is
	// true (matches the Python reference's `top_n: int | None = None`).
	TopN int
	// TopNSet reports whether TopN should be applied. When false, every
	// hit is returned (reordered).
	TopNSet bool
	// RelevanceFn is the per-hit relevance term (default DefaultRelevance
	// when nil).
	RelevanceFn RelevanceFn
	// SimilarityFn is the pairwise diversity term (default
	// DefaultTextSimilarity when nil).
	SimilarityFn SimilarityFn
}

// MMRSelect greedily selects from hits by Maximal Marginal Relevance.
//
// Repeatedly picks the candidate maximizing
//
//	lambdaMult*relevanceFn(candidate) - (1-lambdaMult)*
//	    max(similarityFn(candidate, s) for s in alreadySelected)
//
// (0.0 similarity for the first pick, nothing selected yet) until topN
// hits have been chosen (when opts.TopNSet) or hits is exhausted.
// lambdaMult is opts.LambdaMult when opts.LambdaMultSet, else
// DefaultMMRLambda.
//
// A pure function: no LLM call, no embedding call, no network I/O —
// deterministic given the same hits/relevanceFn/similarityFn. Ties are
// broken by hits' original order (candidates are scanned in that order and
// only a strictly greater score replaces the current best).
//
// opts may be nil (equivalent to &MMRSelectOptions{}, every default
// applies). Returns an error if the effective lambdaMult is outside
// [0.0, 1.0].
func MMRSelect(hits []RagHit, opts *MMRSelectOptions) ([]RagHit, error) {
	if opts == nil {
		opts = &MMRSelectOptions{}
	}
	lambdaMult := DefaultMMRLambda
	if opts.LambdaMultSet {
		lambdaMult = opts.LambdaMult
	}
	if lambdaMult < 0.0 || lambdaMult > 1.0 {
		return nil, fmt.Errorf("lambdaMult must be within [0.0, 1.0], got %v", lambdaMult)
	}
	relevanceFn := opts.RelevanceFn
	if relevanceFn == nil {
		relevanceFn = DefaultRelevance
	}
	similarityFn := opts.SimilarityFn
	if similarityFn == nil {
		similarityFn = DefaultTextSimilarity
	}

	pool := make([]RagHit, len(hits))
	copy(pool, hits)

	limit := len(pool)
	if opts.TopNSet {
		limit = opts.TopN
		if limit < 0 {
			limit = 0
		}
		if limit > len(pool) {
			limit = len(pool)
		}
	}

	selectedIdx := make([]int, 0, limit)
	available := make([]int, len(pool))
	for i := range pool {
		available[i] = i
	}

	for len(available) > 0 && len(selectedIdx) < limit {
		bestI := -1
		bestScore := 0.0
		haveBest := false
		for _, i := range available {
			candidate := pool[i]
			relevance := relevanceFn(candidate)
			maxSim := 0.0
			for _, j := range selectedIdx {
				sim := similarityFn(candidate, pool[j])
				if sim > maxSim {
					maxSim = sim
				}
			}
			score := lambdaMult*relevance - (1.0-lambdaMult)*maxSim
			if !haveBest || score > bestScore {
				haveBest = true
				bestScore = score
				bestI = i
			}
		}
		selectedIdx = append(selectedIdx, bestI)
		for k, v := range available {
			if v == bestI {
				available = append(available[:k], available[k+1:]...)
				break
			}
		}
	}

	out := make([]RagHit, len(selectedIdx))
	for k, i := range selectedIdx {
		out[k] = pool[i]
	}
	return out, nil
}

// MMRSelectForPurposeOptions configures MMRSelectForPurpose — same as
// MMRSelectOptions minus LambdaMult/LambdaMultSet (resolved from purpose
// instead). opts may be nil, equivalent to &MMRSelectForPurposeOptions{}.
type MMRSelectForPurposeOptions struct {
	TopN         int
	TopNSet      bool
	RelevanceFn  RelevanceFn
	SimilarityFn SimilarityFn
}

// MMRSelectForPurpose is MMRSelect with LambdaMult resolved from purpose
// via MMRLambdaForPurpose — the convenience entry point for callers who
// already have RelataDB's governance purpose in hand and want the
// domain-tuned default rather than picking a lambda themselves.
func MMRSelectForPurpose(
	hits []RagHit,
	purpose string,
	opts *MMRSelectForPurposeOptions,
) ([]RagHit, error) {
	if opts == nil {
		opts = &MMRSelectForPurposeOptions{}
	}
	return MMRSelect(hits, &MMRSelectOptions{
		LambdaMult:    MMRLambdaForPurpose(purpose),
		LambdaMultSet: true,
		TopN:          opts.TopN,
		TopNSet:       opts.TopNSet,
		RelevanceFn:   opts.RelevanceFn,
		SimilarityFn:  opts.SimilarityFn,
	})
}
