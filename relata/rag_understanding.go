package relata

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Package-level query-shape dispatch + HyDE + decomposition — RAG epic
// SDK-side query understanding (#4524), extended by #4536 with a
// content-safety pre-filter gate and structured-attribute-filter query
// routing, and by #4535 with aggregation/negation/boolean/ranking SQL
// routing.
//
// Faithful Go port of sdks/python/relata/rag_understanding.py (#4577, epic
// #4576) — closing the deliberate Python-only asymmetry ADR-0298
// introduced ("one SDK carries the canonical implementation... until real
// usage demands porting the loop to them too"). Every verified numeric
// constant (thresholds, defaults, the RRF k formula) matches the Python
// source exactly; SmartRagQuery's branch order (safety gate -> SQL-shape
// check -> decomposition/HyDE) is preserved.
//
// Structural adaptations from the Python source, required by language shape
// rather than a deliberate behavior change:
//
//   - Python exposes separate sync (route_*_query) and async (aroute_*_query)
//     twins. Go has one calling convention (a context.Context-taking function
//     that may block), so each pair collapses to one function here.
//   - Go's regexp package (RE2) has no lookahead/lookbehind, unlike Python's
//     re. The two decomposition-split patterns that used a lookahead in
//     Python are reimplemented in decomposeQuery below using a capturing
//     group for the "must be followed by" anchor plus manual span
//     arithmetic — same split points, verified against the same test cases
//     the Python reference's test suite uses.
//   - Python's **rag_kwargs forwarded to RagClient.query is a dict, so
//     ClassifyQueryShape's per-shape adjustment can tell "caller didn't set
//     this" from "caller explicitly set it". SmartRagOptions uses *int/*bool
//     for the two fields applyQueryShape adjusts (TopK, ExpandWindow) to
//     preserve that same distinction.
//
// Per ADR-0298, RelataDB has no server-side agent loop and orchestration
// lives in the SDK; sdks/python/relata/rag.go's RagClient stays canonical
// and this package's RagClient (rag.go) stays a thin pass-through over the
// frozen /rag/query contract. This file is the query-understanding layer
// *above* that thin pass-through, now shipped here too per epic #4576.

// ---------------------------------------------------------------------------
// 0. Content-safety pre-filter (#4536) — runs before everything else, before
//    any /rag/query or /query call is constructed.
// ---------------------------------------------------------------------------

// DangerousContentPatterns is a reviewed starter set of dangerous-content
// categories -> compiled regex, for a counter-terrorism-intelligence-style
// deployment. Not applied unless a caller explicitly passes it (or its own
// mapping) to CheckContentSafety — the gate itself is off by default.
// Patterns are precision-reviewed to require construction/how-to intent,
// not mere mention, so a news article or countermeasures/deactivation
// discussion about the same subject matter is not falsely refused.
var DangerousContentPatterns = map[string]*regexp.Regexp{
	"weapons_explosives_construction": regexp.MustCompile(
		`(?i)\bhow\s+to\s+(?:build|make|construct|assemble|manufacture)\s+` +
			`(?:an?\s+)?(?:ied|bomb|explosive\w*|pipe\s*bomb|detonator)\b` +
			`|\b(?:ied|bomb|explosive)\s+construction\s+(?:guide|instructions?|manual)\b`,
	),
}

// Refusal is a client-side refusal from the content-safety pre-filter (#4536).
type Refusal struct {
	// Reason is the machine-readable refusal reason code. Always "content_safety".
	Reason string
	// Category is the matched dangerous-content category label.
	Category string
	// Message is a human-readable explanation, safe to surface to callers.
	Message string
}

// CheckContentSafety is a coarse, no-LLM content-safety pre-filter (#4536),
// run ahead of every other technique in this file.
//
// Off by default: a nil/empty patterns map never refuses anything. A
// deployment opts in explicitly by passing a category-label -> compiled
// regex mapping — DangerousContentPatterns is a reviewed starter set, but
// this is a deployment-specific policy decision, so nothing is applied by
// default.
//
// Returns a *Refusal on the first matching category (map iteration order —
// callers needing a deterministic first match should pass a mapping with at
// most one entry, or accept Go's randomized map order for multi-category
// mappings), or nil when nothing matches. This is a coarse pre-filter, not a
// substitute for governance already enforced in-scan by RelataDB's
// ACL/cell-masking (ADR-0299 §11).
func CheckContentSafety(query string, patterns map[string]*regexp.Regexp) *Refusal {
	for category, pattern := range patterns {
		if pattern.MatchString(query) {
			return &Refusal{
				Reason:   "content_safety",
				Category: category,
				Message: fmt.Sprintf(
					"This query was refused by the content-safety pre-filter "+
						"(category: %q) before any retrieval or SQL call was made.",
					category,
				),
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 1. Query-shape dispatch
// ---------------------------------------------------------------------------

// QueryShape is the detected shape of a query text, per ClassifyQueryShape.
type QueryShape string

// Query shapes. SQL-routable shapes route to governed SQL instead of
// retrieval (#4536's AttributeFilter, extended by #4535's four) — a
// pure-retrieval top-k search has no notion of "all"/"not"/"count"/
// "order by".
const (
	QueryShapeAttributeFilter QueryShape = "attribute_filter"
	QueryShapeAggregation     QueryShape = "aggregation"
	QueryShapeNegation        QueryShape = "negation"
	QueryShapeBoolean         QueryShape = "boolean"
	QueryShapeRanking         QueryShape = "ranking"
	QueryShapeConjunction     QueryShape = "conjunction"
	QueryShapeEnumeration     QueryShape = "enumeration"
	QueryShapeSimple          QueryShape = "simple"
)

// SQLRoutableShapes is the set of shapes routed to governed SQL instead of
// retrieval.
var SQLRoutableShapes = map[QueryShape]bool{
	QueryShapeAttributeFilter: true,
	QueryShapeAggregation:     true,
	QueryShapeNegation:        true,
	QueryShapeBoolean:         true,
	QueryShapeRanking:         true,
}

// AttributeFieldKeywords maps a physical/descriptive-attribute keyword to
// the canonical field name its presence implies (#4536's
// structured-attribute-filter shape). Verified against the sampled
// production query "list of persons above 6ft tall with moustache, fair
// complexion". Deliberately example-driven and conservative — callers
// extend/override via the fieldMap argument to ExtractAttributeFilters /
// RouteAttributeFilterQuery.
var AttributeFieldKeywords = map[string]string{
	"tall":              "height",
	"height":            "height",
	"moustache":         "facial_hair",
	"mustache":          "facial_hair",
	"beard":             "facial_hair",
	"clean-shaven":      "facial_hair",
	"complexion":        "complexion",
	"fair":              "complexion",
	"dark-complexioned": "complexion",
	"wheatish":          "complexion",
	"olive-skinned":     "complexion",
	"build":             "build",
	"athletic":          "build",
	"heavyset":          "build",
	"slim":              "build",
	"lean":              "build",
	"scar":              "distinguishing_marks",
	"tattoo":            "distinguishing_marks",
	"birthmark":         "distinguishing_marks",
	"mole":              "distinguishing_marks",
}

// keywordAlternationRegexp builds a `\b(kw1|kw2|...)\b` case-insensitive
// regexp from keywords, longest-first (so a longer keyword wins over a
// shorter prefix of it), mirroring the Python reference's
// `sorted(keywords, key=len, reverse=True)` regex construction.
func keywordAlternationRegexp(keywords []string) *regexp.Regexp {
	sorted := append([]string(nil), keywords...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	escaped := make([]string, len(sorted))
	for i, k := range sorted {
		escaped[i] = regexp.QuoteMeta(k)
	}
	return regexp.MustCompile(`(?i)\b(` + strings.Join(escaped, "|") + `)\b`)
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var attributeFilterRE = keywordAlternationRegexp(mapKeys(AttributeFieldKeywords))

// IsAttributeFilterIntent reports whether query names at least one
// physical/descriptive attribute keyword from AttributeFieldKeywords —
// #4536's structured-attribute-filter shape.
func IsAttributeFilterIntent(query string) bool {
	return attributeFilterRE.MatchString(query)
}

// Conjunction patterns: "who ... and ...", "with X and Y", "both ... and ...".
var conjunctionRE = regexp.MustCompile(`(?i)\bwho\b.*\band\b|\bwith\b\s+\S+\s+\band\b\s+\S+|\bboth\b.*\band\b`)

// Enumeration patterns: a query opening with "which"/"list"/"how many"/etc.
var enumerationRE = regexp.MustCompile(`(?i)^\s*(which|what\s+are\s+all|list|enumerate|name\s+all|how\s+many)\b`)

// EnumerationTopK is the widened top-k enumeration queries route toward,
// past #4514's server default (RagDefaultTopK = 8), giving document-level
// aggregation enough candidates to work with.
const EnumerationTopK = 25

// ---------------------------------------------------------------------------
// 1b. Aggregation/negation/boolean/ranking detection (#4535) — a
//     pure-retrieval top-k search structurally cannot answer these; each
//     routes to governed SQL instead of retrieval.
// ---------------------------------------------------------------------------

// Lexical cues for a countable/aggregate intent.
var aggregationRE = regexp.MustCompile(
	`(?i)\bhow\s+many\b|\bhow\s+much\b|\bcount\s+of\b|\btotal\s+number\s+of\b|` +
		`\btotal\s+count\b|\baverage\b|\bavg\b|\bsum\s+of\b`,
)

// Negation markers adjacent to an entity/attribute reference.
var negationRE = regexp.MustCompile(
	`(?i)\bnot\b|\bexcept\b|\bexcluding\b|\bwithout\b|\bisn't\b|\baren't\b|\bdoesn't\b|\bdon't\b`,
)

// Boolean/set-operation shape: "members of X and Y", "belongs to X or Y".
var booleanRE = regexp.MustCompile(
	`(?i)\b(?:members?\s+of|belongs?\s+to|affiliated\s+with|part\s+of)\s+` +
		`[\w&-]+(?:\s+[\w&-]+){0,3}\s+(?:and|or)\s+[\w&-]+`,
)

// Ranking/top-N shape: "top N", "first N", "most X", "highest"/"lowest".
var rankingRE = regexp.MustCompile(`(?i)\btop\s+\d+\b|\bfirst\s+\d+\b|\bmost\s+\w+\b|\bhighest\b|\blowest\b|\bleast\s+\w+\b`)

// IsAggregationIntent reports whether query carries a countable/aggregate
// lexical cue (#4535, e.g. "how many", "total number of").
func IsAggregationIntent(query string) bool { return aggregationRE.MatchString(query) }

// IsNegationIntent reports whether query carries a negation marker (#4535).
func IsNegationIntent(query string) bool { return negationRE.MatchString(query) }

// IsBooleanIntent reports whether query names multiple entities joined by
// "and"/"or" in a set-operation shape (#4535).
func IsBooleanIntent(query string) bool { return booleanRE.MatchString(query) }

// IsRankingIntent reports whether query carries a ranking/top-N lexical cue (#4535).
func IsRankingIntent(query string) bool { return rankingRE.MatchString(query) }

// ClassifyQueryShape classifies query as a QueryShape via regex — no LLM
// call, cheap enough to run on every query.
//
// Checked in this order: AttributeFilter, Aggregation, Negation, Boolean,
// Ranking, Conjunction, Enumeration, Simple. AttributeFilter and #4535's
// four shapes are checked ahead of Enumeration/Conjunction because their
// trigger words otherwise overlap with Enumeration's opening-word cues
// (e.g. "how many incidents were filed?" opens with an Enumeration-style
// "how many" but must classify as Aggregation so it routes to a real
// COUNT(*) instead of a top-k retrieval-and-hope-the-caller-counts-them
// guess).
func ClassifyQueryShape(query string) QueryShape {
	switch {
	case IsAttributeFilterIntent(query):
		return QueryShapeAttributeFilter
	case IsAggregationIntent(query):
		return QueryShapeAggregation
	case IsNegationIntent(query):
		return QueryShapeNegation
	case IsBooleanIntent(query):
		return QueryShapeBoolean
	case IsRankingIntent(query):
		return QueryShapeRanking
	case conjunctionRE.MatchString(query):
		return QueryShapeConjunction
	case enumerationRE.MatchString(query):
		return QueryShapeEnumeration
	default:
		return QueryShapeSimple
	}
}

// SmartRagOptions mirrors the fields Python's **rag_kwargs forwards to
// RagClient.query for every sub-query call in SmartRagQuery, after
// applyQueryShape's per-shape adjustments are applied on top. TopK and
// ExpandWindow use pointers so an explicit caller value (including an
// explicit zero/false) is distinguishable from "not set" — needed to
// replicate Python's dict-based `setdefault`/max-with-caller-value
// semantics for the Conjunction/Enumeration adjustments.
type SmartRagOptions struct {
	TopK          *int
	Rerank        bool
	SearchMode    string
	EmbeddingSlot string
	Filters       []RagFilter
	AsOf          string
	ExpandWindow  *bool
	GraphHops     int
}

// applyQueryShape returns a copy of opts adjusted for the detected shape.
// Conjunction queries route toward ExpandWindow=true (only if the caller
// did not already set it explicitly). Enumeration queries widen TopK to at
// least EnumerationTopK, taking the max with whatever the caller already
// requested so an explicit larger value is never shrunk.
func applyQueryShape(shape QueryShape, opts SmartRagOptions) SmartRagOptions {
	out := opts
	switch shape {
	case QueryShapeConjunction:
		if out.ExpandWindow == nil {
			t := true
			out.ExpandWindow = &t
		}
	case QueryShapeEnumeration:
		base := RagDefaultTopK
		if out.TopK != nil {
			base = *out.TopK
		}
		widened := base
		if EnumerationTopK > widened {
			widened = EnumerationTopK
		}
		out.TopK = &widened
	}
	return out
}

// ---------------------------------------------------------------------------
// 2. HyDE, with the numeric-intent guard
// ---------------------------------------------------------------------------

// HypothesisGenerator is a caller-supplied hypothetical-answer generator.
// RelataDB runs no LLM (ADR-013) — this is always provided by the
// application, never called by RelataDB itself.
type HypothesisGenerator func(ctx context.Context, query string) (string, error)

// NumericIntentWords are numeric-intent trigger words (verified list from
// the reference toolkit). A query containing one of these usually wants an
// exact number; HyDE's hallucinated hypothetical answer would invent a
// plausible-but-wrong one, so HyDE is skipped entirely for these rather
// than risk it.
var NumericIntentWords = []string{
	"revenue", "percentage", "percent", "count", "how many", "total", "sum",
	"average", "mean", "median", "dosage", "dose", "amount", "quantity",
	"number of", "price", "cost", "rate", "ratio", "margin", "growth rate",
}

var numericIntentRE = keywordAlternationRegexp(NumericIntentWords)

// IsNumericIntent reports whether query contains a NumericIntentWords
// trigger word — the guard ExpandQueryHyde uses to skip HyDE entirely.
func IsNumericIntent(query string) bool { return numericIntentRE.MatchString(query) }

// ExpandQueryHyde returns the HyDE-expanded search text for query, or query
// unchanged when IsNumericIntent trips.
//
// hypothesisFn is called with query and must return a hypothetical answer
// (plain text); that hypothetical answer becomes the query field sent to
// POST /rag/query (#4514), which embeds and searches it server-side — this
// function does not embed anything itself.
func ExpandQueryHyde(ctx context.Context, query string, hypothesisFn HypothesisGenerator) (string, error) {
	if IsNumericIntent(query) {
		return query, nil
	}
	hypothetical, err := hypothesisFn(ctx, query)
	if err != nil {
		return "", err
	}
	if hypothetical == "" {
		return query, nil
	}
	return hypothetical, nil
}

// ---------------------------------------------------------------------------
// 3. Decomposition + RRF merge with auto-scaling k
// ---------------------------------------------------------------------------

// decompositionSplitRE finds a split point at (a) a `?` immediately
// followed by a run of whitespace and a letter, or (b) an "and" conjoining
// two question clauses ("... and when ...", "... and how ..."). The
// trailing letter/word is captured (group 1 or group 2) rather than
// consumed by a lookahead — Go's regexp (RE2) has no lookahead support, so
// DecomposeQuery below trims each match back to the captured group's start
// instead of its end, keeping the anchor character/word as part of the next
// segment (see the file doc comment).
var decompositionSplitRE = regexp.MustCompile(
	`(?i)\?\s+([A-Za-z])|\s+and\s+((?:what|when|where|who|which|how|why))\b`,
)

// DecomposeQuery splits query into independently-retrievable sub-queries.
//
// Returns []string{query} (a single-element slice) when no split point is
// detected — callers should treat len(...) == 1 as "no decomposition
// happened," not as an error.
func DecomposeQuery(query string) []string {
	matches := decompositionSplitRE.FindAllStringSubmatchIndex(query, -1)
	if len(matches) == 0 {
		return []string{query}
	}
	var rawParts []string
	last := 0
	for _, m := range matches {
		// m = [matchStart, matchEnd, g1Start, g1End, g2Start, g2End]
		groupStart := m[1]
		if m[2] != -1 {
			groupStart = m[2]
		} else if m[4] != -1 {
			groupStart = m[4]
		}
		rawParts = append(rawParts, query[last:m[0]])
		last = groupStart
	}
	rawParts = append(rawParts, query[last:])

	out := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{query}
	}
	return out
}

// RrfKForFanout returns the auto-scaling RRF dampening constant for merging
// nSubqueries parallel result lists: max(10, 60/n) (verified in the
// reference toolkit). Deliberately *not* RelataDB's fixed internal
// RRF_K=60 (crates/relata-query/src/hybrid.rs), which fuses BM25(+)vector
// within one /rag/query call — a different merge at a different layer.
func RrfKForFanout(nSubqueries int) (float64, error) {
	if nSubqueries <= 0 {
		return 0, errors.New("relata: nSubqueries must be positive")
	}
	k := 60.0 / float64(nSubqueries)
	if k < 10.0 {
		k = 10.0
	}
	return k, nil
}

// RrfScores computes the per-chunk-id summed RRF contribution
// 1/(k+rank) across responses. Split out from RrfMerge so the exact
// arithmetic (and, in particular, which k it was actually called with) is
// directly assertable in tests without depending on final hit ordering.
func RrfScores(responses []*RagQueryResponse, k float64) map[string]float64 {
	scores := make(map[string]float64)
	for _, response := range responses {
		for i, hit := range response.Hits {
			rank := float64(i + 1)
			scores[hit.ChunkID] += 1.0 / (k + rank)
		}
	}
	return scores
}

// RrfMerge Reciprocal-Rank-Fuses responses (one per sub-query) into a
// single *RagQueryResponse, deduped by ChunkID and re-ordered by the summed
// RRF contribution 1/(k+rank) (see RrfScores).
//
// Each hit's own BM25Score/VectorScore is passed through untouched — RRF
// only decides final ordering/dedup here, it never fuses or overwrites
// those fields.
func RrfMerge(responses []*RagQueryResponse, k float64) *RagQueryResponse {
	scores := RrfScores(responses, k)
	firstSeen := make(map[string]RagHit)
	order := make([]string, 0, len(scores))
	for _, response := range responses {
		for _, hit := range response.Hits {
			if _, ok := firstSeen[hit.ChunkID]; !ok {
				firstSeen[hit.ChunkID] = hit
			}
		}
	}
	for id := range scores {
		order = append(order, id)
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
	hits := make([]RagHit, len(order))
	for i, id := range order {
		hits[i] = firstSeen[id]
	}
	return &RagQueryResponse{Hits: hits}
}

// ---------------------------------------------------------------------------
// SQL-predicate building shared by every routing function below
// ---------------------------------------------------------------------------

// AttributeFilter is a single {field, op, value} predicate — the shape
// SQL-routing extraction produces.
type AttributeFilter struct {
	Field string
	Op    string
	Value any
}

var sqlOpAllowlist = map[string]bool{
	">=": true, "<=": true, "=": true, "ILIKE": true, "NOT ILIKE": true, "IN": true,
}

func sqlLiteralValue(v any) string {
	switch val := v.(type) {
	case bool:
		if val {
			return "TRUE"
		}
		return "FALSE"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(val)
	default:
		return sqlLiteral(fmt.Sprint(val))
	}
}

func filterToSQLPredicate(f AttributeFilter) (string, error) {
	if err := validateIdentifier(f.Field, "field name"); err != nil {
		return "", err
	}
	if !sqlOpAllowlist[f.Op] {
		return "", fmt.Errorf("relata: invalid SQL operator %q", f.Op)
	}
	if f.Op == "IN" {
		values, ok := f.Value.([]any)
		if !ok {
			return "", fmt.Errorf("relata: IN predicate value must be []any")
		}
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = sqlLiteralValue(v)
		}
		return fmt.Sprintf("%s IN (%s)", f.Field, strings.Join(parts, ", ")), nil
	}
	return fmt.Sprintf("%s %s %s", f.Field, f.Op, sqlLiteralValue(f.Value)), nil
}

func filtersToWhereClause(filters []AttributeFilter, joiner string) (string, error) {
	predicates := make([]string, len(filters))
	for i, f := range filters {
		p, err := filterToSQLPredicate(f)
		if err != nil {
			return "", err
		}
		predicates[i] = p
	}
	return strings.Join(predicates, joiner), nil
}

func knownFieldSet(knownFields []string) map[string]bool {
	if knownFields == nil {
		return nil
	}
	out := make(map[string]bool, len(knownFields))
	for _, f := range knownFields {
		out[f] = true
	}
	return out
}

func filterKnownFields(filters []AttributeFilter, known map[string]bool) []AttributeFilter {
	if known == nil {
		return filters
	}
	out := make([]AttributeFilter, 0, len(filters))
	for _, f := range filters {
		if known[f.Field] {
			out = append(out, f)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 4. Structured-attribute-filter routing (#4536)
// ---------------------------------------------------------------------------

// "above/over/taller than N (ft|feet|cm|in|inches)" — the >= half of the
// height-comparison shape.
var attributeAboveRE = regexp.MustCompile(
	`(?i)\b(?:above|over|taller\s+than|greater\s+than|more\s+than)\s+(\d+(?:\.\d+)?)\s*(ft|feet|cm|in|inches)?\b`,
)

// "below/under/shorter than N (ft|feet|cm|in|inches)" — the <= half.
var attributeBelowRE = regexp.MustCompile(
	`(?i)\b(?:below|under|shorter\s+than|less\s+than)\s+(\d+(?:\.\d+)?)\s*(ft|feet|cm|in|inches)?\b`,
)

const feetToCM = 30.48
const inchToCM = 2.54

func heightValueCM(raw, unit string) float64 {
	value, _ := strconv.ParseFloat(raw, 64)
	u := strings.ToLower(unit)
	if u == "" {
		u = "ft"
	}
	switch u {
	case "ft", "feet":
		return roundTo1(value * feetToCM)
	case "in", "inches":
		return roundTo1(value * inchToCM)
	default:
		return value // already centimeters
	}
}

func roundTo1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// ExtractAttributeFilters extracts [{field, op, value}] predicates for an
// attribute-filter-shaped query (#4536).
//
// fieldMap overrides/extends AttributeFieldKeywords (pass nil to use the
// built-in defaults unmodified). Height comparisons (e.g. "above 6ft tall")
// become a >=/<= predicate on the mapped field, converted to centimeters.
// Every other matched keyword becomes an ILIKE predicate. Returns nil when
// nothing in the query matches AttributeFieldKeywords — callers must treat
// that as "could not extract a filter," not an error.
func ExtractAttributeFilters(query string, fieldMap map[string]string) []AttributeFilter {
	keywords := make(map[string]string, len(AttributeFieldKeywords)+len(fieldMap))
	for k, v := range AttributeFieldKeywords {
		keywords[k] = v
	}
	for k, v := range fieldMap {
		keywords[k] = v
	}
	heightField := keywords["height"]
	if heightField == "" {
		heightField = "height"
	}

	var filters []AttributeFilter

	if m := attributeAboveRE.FindStringSubmatch(query); m != nil {
		filters = append(filters, AttributeFilter{Field: heightField, Op: ">=", Value: heightValueCM(m[1], m[2])})
	}
	if m := attributeBelowRE.FindStringSubmatch(query); m != nil {
		filters = append(filters, AttributeFilter{Field: heightField, Op: "<=", Value: heightValueCM(m[1], m[2])})
	}

	seenFields := make(map[string]bool)
	for _, f := range filters {
		seenFields[f.Field] = true
	}
	orderedKeywords := mapKeys(keywords)
	sort.Slice(orderedKeywords, func(i, j int) bool { return len(orderedKeywords[i]) > len(orderedKeywords[j]) })
	for _, keyword := range orderedKeywords {
		field := keywords[keyword]
		// Height is comparison-only (above/below), never ILIKE. A bare
		// field-name mention (e.g. "complexion" -> "complexion") names no
		// actual value, so it must not become a self-referential
		// `complexion ILIKE '%complexion%'` predicate. Every other field
		// takes its first matching keyword only.
		if field == heightField || seenFields[field] || keyword == field {
			continue
		}
		if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(keyword) + `\b`).MatchString(query) {
			filters = append(filters, AttributeFilter{Field: field, Op: "ILIKE", Value: "%" + keyword + "%"})
			seenFields[field] = true
		}
	}

	return filters
}

func attributeFilterSQL(query, objectType string, fieldMap map[string]string, knownFields []string) (string, error) {
	if err := validateIdentifier(objectType, "type"); err != nil {
		return "", err
	}
	filters := filterKnownFields(ExtractAttributeFilters(query, fieldMap), knownFieldSet(knownFields))
	if len(filters) == 0 {
		return "", nil
	}
	where, err := filtersToWhereClause(filters, " AND ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT * FROM %s WHERE %s", objectType, where), nil
}

// RouteQueryOptions is shared by every SQL-routing function below.
type RouteQueryOptions struct {
	Purpose string
	// FieldMap is nil to use built-in defaults (RouteAttributeFilterQuery
	// only) or "no vocabulary" (the #4535 routers, which require it).
	FieldMap map[string]string
	// KnownFields nil means unrestricted; a non-nil (possibly empty) slice
	// restricts routing to schema-known fields.
	KnownFields []string
}

func (o RouteQueryOptions) queryOpts() []QueryOption {
	if o.Purpose == "" {
		return nil
	}
	return []QueryOption{WithPurpose(o.Purpose)}
}

// RouteAttributeFilterQuery routes an attribute-filter-shaped query (#4536)
// to a governed SQL /query call instead of a semantic-similarity retrieval
// guess.
//
// Returns (nil, nil) — never an error for "nothing to route" — when
// ExtractAttributeFilters found nothing, or when opts.KnownFields is
// non-nil and none of the extracted filters' fields are present in it — the
// caller falls back to retrieval in that case rather than silently guessing
// against fields that don't exist on objectType's schema.
func RouteAttributeFilterQuery(ctx context.Context, client *Client, query, objectType string, opts RouteQueryOptions) (*QueryResult, error) {
	sql, err := attributeFilterSQL(query, objectType, opts.FieldMap, opts.KnownFields)
	if err != nil {
		return nil, err
	}
	if sql == "" {
		return nil, nil
	}
	return client.Query(ctx, sql, opts.queryOpts()...)
}

// ---------------------------------------------------------------------------
// 5. Aggregation/negation/boolean/ranking SQL routing (#4535)
// ---------------------------------------------------------------------------
//
// Unlike AttributeFieldKeywords (a general-purpose default for
// physical/descriptive attributes), there is no sensible universal keyword
// vocabulary for these four shapes — callers supply their own fieldMap
// (keyword -> canonical field name, deployment-specific). AGGREGATION is
// the one exception: a bare COUNT(*) needs no domain vocabulary at all, so
// it always attempts SQL routing.

// ExtractKeywordFilters is the generic keyword -> field predicate
// extraction shared by the aggregation/negation/boolean routers (#4535) —
// the same mechanism as ExtractAttributeFilters' ILIKE branch, but against
// a caller-required fieldMap rather than a built-in default.
//
// dedupeFields=true keeps only the first matching keyword per field,
// matching ExtractAttributeFilters' behaviour. RouteBooleanQuery passes
// dedupeFields=false because a boolean shape's whole point is multiple
// predicates over the *same* field.
func ExtractKeywordFilters(query string, fieldMap map[string]string, op string, dedupeFields bool) []AttributeFilter {
	var filters []AttributeFilter
	seenFields := make(map[string]bool)
	orderedKeywords := mapKeys(fieldMap)
	sort.Slice(orderedKeywords, func(i, j int) bool { return len(orderedKeywords[i]) > len(orderedKeywords[j]) })
	for _, keyword := range orderedKeywords {
		field := fieldMap[keyword]
		if dedupeFields && seenFields[field] {
			continue
		}
		if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(keyword) + `\b`).MatchString(query) {
			value := keyword
			if op == "ILIKE" || op == "NOT ILIKE" {
				value = "%" + keyword + "%"
			}
			filters = append(filters, AttributeFilter{Field: field, Op: op, Value: value})
			seenFields[field] = true
		}
	}
	return filters
}

// firstMatchingField returns the canonical field for the first fieldMap
// keyword found in query (longest keyword first), or "" when nothing
// matches — used by RouteRankingQuery to pick the ORDER BY column.
func firstMatchingField(query string, fieldMap map[string]string) string {
	orderedKeywords := mapKeys(fieldMap)
	sort.Slice(orderedKeywords, func(i, j int) bool { return len(orderedKeywords[i]) > len(orderedKeywords[j]) })
	for _, keyword := range orderedKeywords {
		if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(keyword) + `\b`).MatchString(query) {
			return fieldMap[keyword]
		}
	}
	return ""
}

// RouteAggregationQuery routes an aggregation-shaped query (#4535, e.g.
// "how many incidents happened in 2023?") to a governed SELECT COUNT(*)
// instead of a retrieval top-k the caller would have to guess-count from.
//
// Always attempts SQL routing (returns a real *QueryResult, never nil, nil)
// when opts.FieldMap is nil, or when it's given but names nothing found in
// query — a bare COUNT(*) over objectType needs no domain vocabulary.
// Returns (nil, nil) only when opts.FieldMap *did* extract a keyword filter
// but opts.KnownFields rules every extracted field out.
func RouteAggregationQuery(ctx context.Context, client *Client, query, objectType string, opts RouteQueryOptions) (*QueryResult, error) {
	if err := validateIdentifier(objectType, "type"); err != nil {
		return nil, err
	}
	var filters []AttributeFilter
	if opts.FieldMap != nil {
		filters = ExtractKeywordFilters(query, opts.FieldMap, "ILIKE", true)
	}
	if known := knownFieldSet(opts.KnownFields); known != nil && len(filters) > 0 {
		resolved := filterKnownFields(filters, known)
		if len(resolved) == 0 {
			return nil, nil
		}
		filters = resolved
	}
	sql := fmt.Sprintf("SELECT COUNT(*) AS count FROM %s", objectType)
	if len(filters) > 0 {
		where, err := filtersToWhereClause(filters, " AND ")
		if err != nil {
			return nil, err
		}
		sql += " WHERE " + where
	}
	return client.Query(ctx, sql, opts.queryOpts()...)
}

// RouteNegationQuery routes a negation-shaped query (#4535, e.g. "Which
// SIMI members are NOT in custody?") to a governed NOT ILIKE predicate
// instead of retrieval — retrieval has no notion of "not".
//
// Requires opts.FieldMap (no built-in default). Returns (nil, nil) when
// FieldMap is nil, or extracts no keyword, or (with KnownFields given)
// resolves to no schema-known field.
func RouteNegationQuery(ctx context.Context, client *Client, query, objectType string, opts RouteQueryOptions) (*QueryResult, error) {
	if opts.FieldMap == nil {
		return nil, nil
	}
	if err := validateIdentifier(objectType, "type"); err != nil {
		return nil, err
	}
	filters := filterKnownFields(
		ExtractKeywordFilters(query, opts.FieldMap, "NOT ILIKE", true),
		knownFieldSet(opts.KnownFields),
	)
	if len(filters) == 0 {
		return nil, nil
	}
	where, err := filtersToWhereClause(filters, " AND ")
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf("SELECT * FROM %s WHERE %s", objectType, where)
	return client.Query(ctx, sql, opts.queryOpts()...)
}

// Matches the conjunction word actually used in a Boolean-shaped query.
var booleanConjunctionWordRE = regexp.MustCompile(`(?i)\b(and|or)\b`)

// RouteBooleanQuery routes a boolean/set-operation-shaped query (#4535,
// e.g. "Members of SIMI AND LeT") to governed SQL predicates joined by the
// query's own AND/OR instead of a semantic-similarity guess.
//
// Requires opts.FieldMap and at least two resolved keyword matches —
// anything less returns (nil, nil) and the caller falls back to retrieval.
func RouteBooleanQuery(ctx context.Context, client *Client, query, objectType string, opts RouteQueryOptions) (*QueryResult, error) {
	if opts.FieldMap == nil {
		return nil, nil
	}
	if err := validateIdentifier(objectType, "type"); err != nil {
		return nil, err
	}
	filters := filterKnownFields(
		ExtractKeywordFilters(query, opts.FieldMap, "=", false),
		knownFieldSet(opts.KnownFields),
	)
	if len(filters) < 2 {
		return nil, nil
	}
	joiner := " AND "
	if m := booleanConjunctionWordRE.FindStringSubmatch(query); m != nil && strings.EqualFold(m[1], "or") {
		joiner = " OR "
	}
	where, err := filtersToWhereClause(filters, joiner)
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf("SELECT * FROM %s WHERE %s", objectType, where)
	return client.Query(ctx, sql, opts.queryOpts()...)
}

// Explicit "top N" / "first N" count, when given.
var rankingNRE = regexp.MustCompile(`(?i)\btop\s+(\d+)\b|\bfirst\s+(\d+)\b`)

// "highest"/"most" default to descending; no explicit N given.
var rankingAscendingRE = regexp.MustCompile(`(?i)\blowest\b|\bleast\b`)

// DefaultRankingLimit is the default LIMIT for a ranking query with no
// explicit "top N"/"first N".
const DefaultRankingLimit = 10

func rankingLimit(query string) int {
	m := rankingNRE.FindStringSubmatch(query)
	if m == nil {
		return DefaultRankingLimit
	}
	if m[1] != "" {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	n, _ := strconv.Atoi(m[2])
	return n
}

// RouteRankingQuery routes a ranking/top-N-shaped query (#4535, e.g. "Top 5
// most active members") to a governed ORDER BY ... LIMIT N instead of
// retrieval.
//
// Requires opts.FieldMap naming the sortable attribute — returns (nil, nil)
// when FieldMap is nil, resolves to no field, or (with KnownFields given)
// the resolved field isn't schema-known.
func RouteRankingQuery(ctx context.Context, client *Client, query, objectType string, opts RouteQueryOptions) (*QueryResult, error) {
	if opts.FieldMap == nil {
		return nil, nil
	}
	orderField := firstMatchingField(query, opts.FieldMap)
	if orderField == "" {
		return nil, nil
	}
	if known := knownFieldSet(opts.KnownFields); known != nil && !known[orderField] {
		return nil, nil
	}
	if err := validateIdentifier(objectType, "type"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(orderField, "order field"); err != nil {
		return nil, err
	}
	direction := "DESC"
	if rankingAscendingRE.MatchString(query) {
		direction = "ASC"
	}
	sql := fmt.Sprintf("SELECT * FROM %s ORDER BY %s %s LIMIT %d", objectType, orderField, direction, rankingLimit(query))
	return client.Query(ctx, sql, opts.queryOpts()...)
}

// ---------------------------------------------------------------------------
// 5b. ENUMERATION large-result-set routing (#4535, second half)
// ---------------------------------------------------------------------------

// ErrEnumerationTimeout is returned by RouteEnumerationQuery when the
// background /rag/export operation does not complete within the poll
// timeout.
var ErrEnumerationTimeout = errors.New("relata: /rag/export operation did not complete within the poll timeout")

// EnumerationPollInterval is the default interval between
// GET /v1/operations/{id} polls.
const EnumerationPollInterval = 500 * time.Millisecond

// EnumerationPollTimeout is the default ceiling on total poll time before
// giving up with ErrEnumerationTimeout — a large enumeration is a
// background job, but it must not poll forever.
const EnumerationPollTimeout = 300 * time.Second

// LargeExportResult is the result of a completed POST /rag/export
// operation (#4535).
//
// Exactly one of two shapes, distinguished by IsFileBacked: Inline
// (RowCount at/under the server's threshold): Data carries every matching
// row; Bucket/Key are empty. File-backed (RowCount over the threshold):
// Preview carries a small, explicitly-labeled sample (never presented as
// complete); Bucket/Key/ETag name the governed S3Object holding the full
// result, readable back via the S3-compatible door.
type LargeExportResult struct {
	RowCount    int
	Columns     []string
	Data        []map[string]any
	Preview     []map[string]any
	PreviewNote string
	Bucket      string
	Key         string
	ETag        string
	ContentType string
	SizeBytes   int64
}

// IsFileBacked reports whether the result was too large for an inline
// response and was written as a governed S3Object instead.
func (r *LargeExportResult) IsFileBacked() bool { return r.Bucket != "" }

func largeExportResultFromBody(body map[string]any) *LargeExportResult {
	get := func(k string) any { return body[k] }
	asInt := func(v any) int {
		f, _ := v.(float64)
		return int(f)
	}
	asInt64 := func(v any) int64 {
		f, _ := v.(float64)
		return int64(f)
	}
	asStr := func(v any) string {
		s, _ := v.(string)
		return s
	}
	asStrSlice := func(v any) []string {
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	asRowSlice := func(v any) []map[string]any {
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		out := make([]map[string]any, 0, len(arr))
		for _, e := range arr {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return &LargeExportResult{
		RowCount:    asInt(get("row_count")),
		Columns:     asStrSlice(get("columns")),
		Data:        asRowSlice(get("data")),
		Preview:     asRowSlice(get("preview")),
		PreviewNote: asStr(get("preview_note")),
		Bucket:      asStr(get("bucket")),
		Key:         asStr(get("key")),
		ETag:        asStr(get("etag")),
		ContentType: asStr(get("content_type")),
		SizeBytes:   asInt64(get("size_bytes")),
	}
}

func enumerationExportSQL(query, objectType string, fieldMap map[string]string, knownFields []string) (string, error) {
	if err := validateIdentifier(objectType, "type"); err != nil {
		return "", err
	}
	var filters []AttributeFilter
	if fieldMap != nil {
		filters = ExtractKeywordFilters(query, fieldMap, "ILIKE", true)
	}
	if known := knownFieldSet(knownFields); known != nil && len(filters) > 0 {
		resolved := filterKnownFields(filters, known)
		if len(resolved) == 0 {
			return "", nil
		}
		filters = resolved
	}
	sql := fmt.Sprintf("SELECT * FROM %s", objectType)
	if len(filters) > 0 {
		where, err := filtersToWhereClause(filters, " AND ")
		if err != nil {
			return "", err
		}
		sql += " WHERE " + where
	}
	return sql, nil
}

// RouteEnumerationOptions configures RouteEnumerationQuery.
type RouteEnumerationOptions struct {
	FieldMap    map[string]string
	KnownFields []string
	// KeyFilter/DateFrom/DateTo are optional, filename-only hints (#4535's
	// "human-legible filename" requirement) — they do not affect the query
	// itself.
	KeyFilter    string
	DateFrom     string
	DateTo       string
	PollInterval time.Duration
	PollTimeout  time.Duration
}

// RouteEnumerationQuery routes an ENUMERATION-shaped query (#4535, e.g.
// "give me all calls made by 9800004040 last year") to POST /rag/export
// instead of a widened-topK retrieval guess that can never be exhaustive.
//
// Returns (nil, nil) — caller should fall back to retrieval — only when
// opts.FieldMap extracted a keyword whose field isn't in opts.KnownFields —
// otherwise always attempts the export (a bare "give me all X" needs no
// domain vocabulary).
//
// Blocks synchronously (or until ctx is cancelled), polling
// GET /v1/operations/{id} every opts.PollInterval until the background
// export completes. Returns ErrEnumerationTimeout if it has not completed
// within opts.PollTimeout, or whatever error the server reports (e.g. a
// tripped row-cap backstop surfaced unchanged as a failed operation — this
// routing does not bypass that cap).
func RouteEnumerationQuery(ctx context.Context, client *Client, query, objectType string, opts RouteEnumerationOptions) (*LargeExportResult, error) {
	sql, err := enumerationExportSQL(query, objectType, opts.FieldMap, opts.KnownFields)
	if err != nil {
		return nil, err
	}
	if sql == "" {
		return nil, nil
	}

	payload := map[string]any{"sql": sql, "type": objectType}
	if opts.KeyFilter != "" {
		payload["key_filter"] = opts.KeyFilter
	}
	if opts.DateFrom != "" {
		payload["date_from"] = opts.DateFrom
	}
	if opts.DateTo != "" {
		payload["date_to"] = opts.DateTo
	}

	var op map[string]any
	if err := client.postJSON(ctx, "/rag/export", payload, &op); err != nil {
		return nil, err
	}
	operationID, _ := op["operation_id"].(string)
	if operationID == "" {
		return largeExportResultFromBody(op), nil
	}

	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = EnumerationPollInterval
	}
	pollTimeout := opts.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = EnumerationPollTimeout
	}
	deadline := time.Now().Add(pollTimeout)
	for {
		var status map[string]any
		if err := client.get(ctx, "/v1/operations/"+operationID, &status); err != nil {
			return nil, err
		}
		if s, _ := status["status"].(string); s != "running" {
			return largeExportResultFromBody(status), nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("relata: /rag/export operation %s did not complete within %s: %w", operationID, pollTimeout, ErrEnumerationTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// ---------------------------------------------------------------------------
// Entry point — composes everything ahead of the first /rag/query call
// ---------------------------------------------------------------------------

// SmartRagQueryResult is the result of SmartRagQuery — extends the plain
// RagQueryResponse shape with the content-safety/SQL-routing outcome.
type SmartRagQueryResult struct {
	Hits                []RagHit
	Total               int
	Refused             *Refusal
	SQLResult           *QueryResult
	LowConfidence       bool
	LowConfidenceReason string
}

// IsRefused reports whether the client-side content-safety gate (#4536)
// refused this query before any call was made.
func (r *SmartRagQueryResult) IsRefused() bool { return r.Refused != nil }

// IsSQLRouted reports whether a structured-attribute-filter/#4535 router
// answered via SQL instead of retrieval.
func (r *SmartRagQueryResult) IsSQLRouted() bool { return r.SQLResult != nil }

// SmartRagQueryOptions configures SmartRagQuery.
type SmartRagQueryOptions struct {
	Purpose string
	// HypothesisFn, when non-nil, HyDE-expands each sub-query's search text
	// before the call (skipped per sub-query when IsNumericIntent trips).
	HypothesisFn HypothesisGenerator
	// ContentSafetyPatterns is nil (the default) to disable the gate
	// entirely, or DangerousContentPatterns / a deployment's own mapping to
	// opt in (#4536).
	ContentSafetyPatterns map[string]*regexp.Regexp
	// AttributeFieldMap overrides/extends AttributeFieldKeywords for
	// #4536's structured-attribute-filter routing.
	AttributeFieldMap map[string]string
	// AttributeKnownFields restricts attribute-filter routing to fields
	// actually present on objectType's schema (#4536). nil = unrestricted.
	AttributeKnownFields []string
	// StructuredFieldMap is the keyword -> canonical field name for #4535's
	// aggregation/negation/boolean/ranking routing.
	StructuredFieldMap map[string]string
	// StructuredKnownFields restricts #4535's routing to fields actually
	// present on objectType's schema. nil = unrestricted.
	StructuredKnownFields []string
	// RagOptions is forwarded to RagClient.Query for every sub-query call,
	// after applyQueryShape's per-shape adjustments are applied on top.
	RagOptions SmartRagOptions
}

func ptrBool(v *bool) bool {
	if v == nil {
		return false
	}
	return *v
}

func ptrInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// SmartRagQuery runs query through the content-safety gate, structured-
// attribute routing, decomposition, per-sub-query shape dispatch, and
// optional HyDE, then issues the (possibly several, concurrent)
// /rag/query calls via ragClient and RRF-merges the results.
//
// Sub-query fan-out runs concurrently via goroutines — the Go equivalent of
// the Python reference's async twin (asmart_rag_query); Go's goroutines
// have no thread-pool-size knob to mirror the sync twin's max_workers.
//
// Refused is set (empty Hits) when the content-safety gate matched, before
// any /rag/query or /query call was made. SQLResult is set (empty Hits)
// when an attribute-filter/aggregation/negation/boolean/ranking-shaped
// query was routed to SQL instead of retrieval. Otherwise this is the
// (possibly RRF-merged) retrieval result — with LowConfidence set when a
// SQL-routable shape could not be routed and fell back to retrieval.
func SmartRagQuery(ctx context.Context, ragClient *RagClient, query, objectType string, opts SmartRagQueryOptions) (*SmartRagQueryResult, error) {
	if refusal := CheckContentSafety(query, opts.ContentSafetyPatterns); refusal != nil {
		return &SmartRagQueryResult{Hits: []RagHit{}, Refused: refusal}, nil
	}

	lowConfidence := false
	shape := ClassifyQueryShape(query)
	if SQLRoutableShapes[shape] {
		client := ragClient.c
		routeOpts := RouteQueryOptions{Purpose: opts.Purpose}
		if shape == QueryShapeAttributeFilter {
			routeOpts.FieldMap = opts.AttributeFieldMap
			routeOpts.KnownFields = opts.AttributeKnownFields
		} else {
			routeOpts.FieldMap = opts.StructuredFieldMap
			routeOpts.KnownFields = opts.StructuredKnownFields
		}

		var sqlResult *QueryResult
		var err error
		switch shape {
		case QueryShapeAttributeFilter:
			sqlResult, err = RouteAttributeFilterQuery(ctx, client, query, objectType, routeOpts)
		case QueryShapeAggregation:
			sqlResult, err = RouteAggregationQuery(ctx, client, query, objectType, routeOpts)
		case QueryShapeNegation:
			sqlResult, err = RouteNegationQuery(ctx, client, query, objectType, routeOpts)
		case QueryShapeBoolean:
			sqlResult, err = RouteBooleanQuery(ctx, client, query, objectType, routeOpts)
		default: // QueryShapeRanking
			sqlResult, err = RouteRankingQuery(ctx, client, query, objectType, routeOpts)
		}
		if err != nil {
			return nil, err
		}
		if sqlResult != nil {
			return &SmartRagQueryResult{Hits: []RagHit{}, SQLResult: sqlResult}, nil
		}
		lowConfidence = true
	}

	subQueries := DecomposeQuery(query)

	run := func(subQuery string) (*RagQueryResponse, error) {
		subShape := ClassifyQueryShape(subQuery)
		callOpts := applyQueryShape(subShape, opts.RagOptions)
		searchText := subQuery
		if opts.HypothesisFn != nil {
			expanded, err := ExpandQueryHyde(ctx, subQuery, opts.HypothesisFn)
			if err != nil {
				return nil, err
			}
			searchText = expanded
		}
		req := RagQueryRequest{
			Query:         searchText,
			Type:          objectType,
			Purpose:       opts.Purpose,
			TopK:          ptrInt(callOpts.TopK),
			Rerank:        callOpts.Rerank,
			SearchMode:    callOpts.SearchMode,
			EmbeddingSlot: callOpts.EmbeddingSlot,
			Filters:       callOpts.Filters,
			AsOf:          callOpts.AsOf,
			ExpandWindow:  ptrBool(callOpts.ExpandWindow),
			GraphHops:     callOpts.GraphHops,
		}
		return ragClient.Query(ctx, req)
	}

	var merged *RagQueryResponse
	if len(subQueries) == 1 {
		resp, err := run(subQueries[0])
		if err != nil {
			return nil, err
		}
		merged = resp
	} else {
		responses := make([]*RagQueryResponse, len(subQueries))
		errs := make([]error, len(subQueries))
		var wg sync.WaitGroup
		for i, sq := range subQueries {
			wg.Add(1)
			go func(i int, sq string) {
				defer wg.Done()
				resp, err := run(sq)
				responses[i] = resp
				errs[i] = err
			}(i, sq)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}
		k, err := RrfKForFanout(len(subQueries))
		if err != nil {
			return nil, err
		}
		merged = RrfMerge(responses, k)
	}

	result := &SmartRagQueryResult{Hits: merged.Hits, Total: len(merged.Hits)}
	if lowConfidence {
		result.LowConfidence = true
		result.LowConfidenceReason = fmt.Sprintf(
			"%s-shaped query could not be routed to SQL "+
				"(no matching canonical field on %q); fell back to retrieval (#4536/#4535)",
			shape, objectType,
		)
	}
	return result, nil
}
