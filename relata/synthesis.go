package relata

import (
	"fmt"
	"regexp"
	"strings"
)

// Package-level doc: citation-injected synthesis + post-synthesis
// faithfulness scoring — highest-priority SDK ticket in the RAG epic
// (#4527), ported from the canonical sdks/python/relata/synthesis.py
// reference implementation (#4579 — read that file in full before changing
// this one).
//
// Mechanism (mirrors the toolkit's confirmed-live pattern — NOT the
// platform system's dead services/rag/ code, which is never referenced or
// ported here):
//
//  1. Inline citation injection — the synthesis LLM call is prompted to
//     attach a `[chunk_id]` marker to each claim as it writes it (not
//     appended afterward as a separate pass), grounded in the
//     citation-grade fields /rag/query (#4514, ADR-0299) already returns on
//     every RagHit: ChunkID, SectionPath, PageStart/PageEnd. Every marker in
//     the raw completion is resolved against the actual hit set the answer
//     was synthesized from — a marker that doesn't match a real ChunkID is
//     stripped and never surfaced as a Citation, so a fabricated citation
//     cannot reach the caller by construction.
//  2. Post-synthesis faithfulness pass — a second, independent LLM call
//     splits the synthesized answer into sentences and entailment-checks
//     each one against the chunk(s) it cites (or the full retrieved set,
//     for an uncited sentence). A sentence that fails entailment is marked
//     with UnsupportedMarker (default "[unsupported]") rather than silently
//     returned as fact. This runs by default (FaithfulnessCheck unset ==
//     true), not as an opt-in.
//
// RelataDB has no server-side agent loop (ADR-013) — orchestration,
// including both LLM calls above, lives in the SDK and is supplied by the
// caller via LLM/FaithfulnessLLM/EntailmentFn. This file owns only the
// prompting, citation resolution, sentence splitting, and faithfulness
// bookkeeping around those calls; it is provider-agnostic by design.

// LlmFn is a single free-text completion call: prompt in, raw text out (or
// an error). The SDK is intentionally provider-agnostic (ADR-013) — callers
// wire in OpenAI/Anthropic/local models etc.
type LlmFn func(prompt string) (string, error)

// EntailmentFn is a sentence-level entailment check: does evidence support
// sentence? Defaults to an LLM-backed check built from LLM (or
// FaithfulnessLLM when given) — see Synthesize.
type EntailmentFn func(sentence string, evidence []string) (bool, error)

// DefaultUnsupportedMarker is the default marker appended to a sentence
// that fails the faithfulness check.
const DefaultUnsupportedMarker = "[unsupported]"

var citationRe = regexp.MustCompile(`\[([A-Za-z0-9_\-:.]+)\]`)

// Citation is a citation that traced back to a real retrieved chunk.
//
// Only ever constructed from an actual RagHit in the response a Synthesize
// call was given — never from an unresolved `[marker]` in raw LLM output.
// This is what makes a fabricated citation impossible by construction
// (#4527 acceptance criterion).
type Citation struct {
	ChunkID     string   `json:"chunk_id"`
	ReportID    string   `json:"report_id"`
	SectionPath []string `json:"section_path"`
	PageStart   int      `json:"page_start"`
	PageEnd     int      `json:"page_end"`
}

// SynthesizedSentence is one sentence of the synthesized answer, with its
// resolved citations and faithfulness verdict.
type SynthesizedSentence struct {
	// Text is the rendered sentence, including UnsupportedMarker if
	// flagged.
	Text      string     `json:"text"`
	Citations []Citation `json:"citations"`
	// Supported is false when the post-synthesis faithfulness pass
	// rejected this sentence.
	Supported bool `json:"supported"`
}

// SynthesisResult is the result of Synthesize — a citation-injected answer
// plus per-sentence faithfulness verdicts.
type SynthesisResult struct {
	// Answer is the full rendered answer, unsupported sentences marked.
	Answer    string                `json:"answer"`
	Sentences []SynthesizedSentence `json:"sentences"`
	// Citations are deduplicated across the whole answer.
	Citations []Citation `json:"citations"`
	// UnsupportedCount is the number of sentences that failed entailment.
	UnsupportedCount int `json:"unsupported_count"`
}

// HasUnsupportedClaims is true when at least one sentence failed the
// faithfulness pass.
func (r SynthesisResult) HasUnsupportedClaims() bool {
	return r.UnsupportedCount > 0
}

func hitIndex(hits []RagHit) map[string]RagHit {
	idx := make(map[string]RagHit, len(hits))
	for _, hit := range hits {
		idx[hit.ChunkID] = hit
	}
	return idx
}

// BuildSynthesisPrompt builds the synthesis LLM prompt: retrieved chunks +
// inline-citation instructions grounded in #4514's citation-grade fields.
//
// Mirrors the toolkit's live pattern — citations are requested inline,
// attached to each claim as the model writes it, not appended afterward as
// a separate pass.
func BuildSynthesisPrompt(query string, hits []RagHit) string {
	blocks := make([]string, 0, len(hits))
	for _, hit := range hits {
		section := "n/a"
		if len(hit.SectionPath) > 0 {
			section = strings.Join(hit.SectionPath, " > ")
		}
		blocks = append(blocks, fmt.Sprintf(
			"[%s] (section %s, p.%d-%d)\n%s",
			hit.ChunkID, section, hit.PageStart, hit.PageEnd, hit.Text,
		))
	}
	context := strings.Join(blocks, "\n\n")
	exampleID := "chunk-id"
	if len(hits) > 0 {
		exampleID = hits[0].ChunkID
	}
	return "Answer the question using ONLY the evidence chunks below. Every " +
		"factual claim MUST be immediately followed by the bracketed chunk " +
		"id it came from, e.g. 'RelataDB fuses BM25 and vector retrieval " +
		"natively [" + exampleID + "]'. Never invent a chunk id that is not " +
		"listed below. If the evidence does not answer the question, say so " +
		"plainly instead of guessing.\n\n" +
		"Question: " + query + "\n\nEvidence:\n" + context + "\n\nAnswer:"
}

// splitSentences is a good-enough sentence splitter for synthesis output:
// split on sentence-terminal punctuation followed by whitespace and an
// uppercase/digit/citation-marker start. Not a full NLP tokenizer — this
// module never claims linguistic precision, only enough granularity for
// per-claim faithfulness marking.
//
// Go's RE2 engine (regexp package) has no lookaround support, so this
// mirrors the Python reference's `(?<=[.!?])\s+(?=[A-Z0-9\[])` regex with an
// explicit scan instead of a single lookaround pattern.
func splitSentences(text string) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	runes := []rune(trimmed)
	var out []string
	start := 0
	i := 0
	for i < len(runes) {
		c := runes[i]
		if c == '.' || c == '!' || c == '?' {
			// Consume the run of whitespace immediately after the
			// terminator, then check the next rune starts a new sentence.
			j := i + 1
			wsEnd := j
			for wsEnd < len(runes) && isSpace(runes[wsEnd]) {
				wsEnd++
			}
			if wsEnd > j && wsEnd < len(runes) && startsSentence(runes[wsEnd]) {
				out = append(out, strings.TrimSpace(string(runes[start:j])))
				start = wsEnd
				i = wsEnd
				continue
			}
		}
		i++
	}
	if start < len(runes) {
		tail := strings.TrimSpace(string(runes[start:]))
		if tail != "" {
			out = append(out, tail)
		}
	}
	return out
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}

func startsSentence(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '['
}

// resolveCitationsInSentence rewrites every `[marker]` in sentence: a
// marker matching a real ChunkID becomes a resolved Citation; anything else
// is stripped from the visible text so it can never masquerade as one.
func resolveCitationsInSentence(sentence string, index map[string]RagHit) (string, []Citation) {
	var citations []Citation
	resolved := citationRe.ReplaceAllStringFunc(sentence, func(match string) string {
		token := citationRe.FindStringSubmatch(match)[1]
		hit, ok := index[token]
		if !ok {
			return ""
		}
		citations = append(citations, Citation{
			ChunkID:     hit.ChunkID,
			ReportID:    hit.ReportID,
			SectionPath: hit.SectionPath,
			PageStart:   hit.PageStart,
			PageEnd:     hit.PageEnd,
		})
		return "[" + hit.ChunkID + "]"
	})
	resolved = strings.Join(strings.Fields(resolved), " ")
	return resolved, citations
}

// defaultEntailmentFn builds an EntailmentFn that asks llm a yes/no
// entailment question — the default second LLM call for the faithfulness
// pass.
func defaultEntailmentFn(llm LlmFn) EntailmentFn {
	return func(sentence string, evidence []string) (bool, error) {
		if len(evidence) == 0 {
			// Nothing to check the claim against at all: it cannot be
			// entailed by an empty evidence set, so it is unsupported by
			// definition rather than trivially "passing".
			return false, nil
		}
		lines := make([]string, 0, len(evidence))
		for _, e := range evidence {
			lines = append(lines, "- "+e)
		}
		prompt := "Evidence:\n" + strings.Join(lines, "\n\n") + "\n\n" +
			"Claim: " + sentence + "\n\n" +
			"Does the evidence above support this claim? Answer with a " +
			"single word, YES or NO."
		raw, err := llm(prompt)
		if err != nil {
			return false, err
		}
		raw = strings.ToLower(strings.TrimSpace(raw))
		return strings.HasPrefix(raw, "yes"), nil
	}
}

// SynthesizeOptions configures Synthesize.
type SynthesizeOptions struct {
	// LLM is the text-completion callable used for the synthesis pass.
	// Called once with the citation-injection prompt built by
	// BuildSynthesisPrompt. Required.
	LLM LlmFn
	// FaithfulnessCheckSet/FaithfulnessCheck together let the zero value of
	// FaithfulnessCheck (false) still mean "run the check" — Go has no
	// three-state bool, and #4527's acceptance criterion requires the
	// faithfulness pass on by default, not opt-in. Use
	// NewSynthesizeOptions or set FaithfulnessCheckSet=true explicitly to
	// disable it.
	FaithfulnessCheck    bool
	FaithfulnessCheckSet bool
	// FaithfulnessLLM is the completion callable for the entailment pass;
	// defaults to LLM when unset (same model, a different prompt — still a
	// second, independent call per sentence).
	FaithfulnessLLM LlmFn
	// EntailmentFn overrides the entailment check entirely (e.g. to use a
	// purpose-built classifier instead of an LLM prompt). Takes precedence
	// over FaithfulnessLLM/LLM when given.
	EntailmentFn EntailmentFn
	// UnsupportedMarker is the suffix appended to a sentence that fails the
	// faithfulness check. Defaults to DefaultUnsupportedMarker when empty.
	UnsupportedMarker string
}

// DisableFaithfulnessCheck returns a copy of opts with the faithfulness
// pass turned off. FaithfulnessCheck is on by default (#4527 AC) — Go has
// no three-state bool, so this explicit setter is how a caller opts out,
// mirroring the Python reference's `faithfulness_check=False`.
func (o SynthesizeOptions) DisableFaithfulnessCheck() SynthesizeOptions {
	o.FaithfulnessCheck = false
	o.FaithfulnessCheckSet = true
	return o
}

func (o SynthesizeOptions) faithfulnessCheckEnabled() bool {
	if !o.FaithfulnessCheckSet {
		return true
	}
	return o.FaithfulnessCheck
}

// Synthesize synthesizes a governed, citation-injected answer from a
// /rag/query response (#4514), then faithfulness-checks it (#4527).
func Synthesize(query string, response RagQueryResponse, opts SynthesizeOptions) (*SynthesisResult, error) {
	if opts.LLM == nil {
		return nil, fmt.Errorf("relata: Synthesize requires SynthesizeOptions.LLM")
	}

	hits := response.Hits
	index := hitIndex(hits)
	prompt := BuildSynthesisPrompt(query, hits)
	rawAnswer, err := opts.LLM(prompt)
	if err != nil {
		return nil, err
	}

	checkFn := opts.EntailmentFn
	if checkFn == nil {
		llmForCheck := opts.FaithfulnessLLM
		if llmForCheck == nil {
			llmForCheck = opts.LLM
		}
		checkFn = defaultEntailmentFn(llmForCheck)
	}

	unsupportedMarker := opts.UnsupportedMarker
	if unsupportedMarker == "" {
		unsupportedMarker = DefaultUnsupportedMarker
	}
	faithfulnessCheck := opts.faithfulnessCheckEnabled()

	var sentences []SynthesizedSentence
	var rendered []string
	seenChunkIDs := make(map[string]bool)
	var dedupedCitations []Citation
	unsupported := 0

	for _, rawSentence := range splitSentences(rawAnswer) {
		text, citations := resolveCitationsInSentence(rawSentence, index)
		if text == "" {
			continue
		}

		supported := true
		if faithfulnessCheck {
			var evidence []string
			if len(citations) > 0 {
				for _, c := range citations {
					evidence = append(evidence, index[c.ChunkID].Text)
				}
			} else {
				for _, h := range hits {
					evidence = append(evidence, h.Text)
				}
			}
			supported, err = checkFn(text, evidence)
			if err != nil {
				return nil, err
			}
			if !supported {
				unsupported++
				text = text + " " + unsupportedMarker
			}
		}

		sentences = append(sentences, SynthesizedSentence{
			Text:      text,
			Citations: citations,
			Supported: supported,
		})
		rendered = append(rendered, text)
		for _, citation := range citations {
			if !seenChunkIDs[citation.ChunkID] {
				seenChunkIDs[citation.ChunkID] = true
				dedupedCitations = append(dedupedCitations, citation)
			}
		}
	}

	return &SynthesisResult{
		Answer:           strings.Join(rendered, " "),
		Sentences:        sentences,
		Citations:        dedupedCitations,
		UnsupportedCount: unsupported,
	}, nil
}
