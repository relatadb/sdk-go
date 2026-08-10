package relata

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// coref.go — Session-scoped coreference (anaphora) resolution — RAG epic
// (#4530). Port of sdks/python/relata/coref.py (#4580, TS/Go parity).
//
// Multi-turn conversational callers of /rag/query (#4514) lose their
// subject across turns: "What is India's capital?" then "Where is it
// located?" has no resolvable antecedent for "it" once the second query
// reaches retrieval in isolation, because /rag/query is deliberately
// stateless and LLM-free per ADR-0299 — that contract does not change here.
//
// This module is the SDK-side fix: a bounded state object, not a replayed
// transcript. After each turn the caller tells CorefResolver the resolved
// subject (typically the winning hit's first EntityIDs entry — see
// SubjectFromHit); before the next turn the caller runs the query through
// CorefResolver.Resolve, which only pays a network cost when a cheap
// regex-level anaphora check trips.
//
// Storage is a governed MemoryItem via POST /memory/remember /
// POST /memory/consolidate (Memory) keyed by session id — not an SDK-local
// cache of the subject value itself, so a second process picking up the
// same session id still resolves correctly. RelataDB's bi-temporal
// supersession means each new subject naturally supersedes the last via
// consolidate rather than accumulating: the state is structurally one row
// per session, never a growing transcript.
//
// Example:
//
//	memory, _ := relata.NewMemory(baseURL, "research", nil)
//	coref := relata.NewCorefResolver(memory)
//
//	resp, _ := ragClient.Query(ctx, relata.RagQueryRequest{
//	    Query: "What is India's capital?", Type: "DocumentChunk", Purpose: "research",
//	})
//	if len(resp.Hits) > 0 {
//	    if subject, ok := relata.SubjectFromHit(resp.Hits[0]); ok {
//	        coref.RememberSubject(ctx, "session-1", subject)
//	    }
//	}
//
//	// Next turn — "it" has no local antecedent, gets rewritten.
//	resolved, _ := coref.Resolve(ctx, "Where is it located?", "session-1")
//	// resolved == "Where is India located?"

// corefPronouns are pronouns with no fixed antecedent that a stateless
// single-shot query cannot resolve on its own. Deliberately small and
// cheap — this is a regex-level guard, not an NLP coreference model.
var corefPronouns = map[string]bool{
	"it":    true,
	"there": true,
	"that":  true,
	"this":  true,
	"they":  true,
	"them":  true,
	"these": true,
	"those": true,
}

// corefNotAntecedents are words that look like a local antecedent
// (capitalised, mid-sentence) but aren't proper nouns — excluded so a
// stray "I" or a sentence-initial capital doesn't suppress a real trip.
var corefNotAntecedents = map[string]bool{"i": true}

// corefMemoryClass is the memory_class used for coref-state rows —
// "episodic" matches the existing turn-scoped-fact convention Memory.Add's
// default doc comment describes.
const corefMemoryClass = "episodic"

var corefPronounRe = regexp.MustCompile(`(?i)\b(` + corefPronounAlternation() + `)\b`)

// corefPronounAlternation builds the regex alternation, longest-first (same
// ordering as the Python reference) so the pattern is deterministic even
// though it makes no practical difference for this fixed whole-word set.
func corefPronounAlternation() string {
	words := make([]string, 0, len(corefPronouns))
	for w := range corefPronouns {
		words = append(words, w)
	}
	sort.Slice(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	return strings.Join(words, "|")
}

// corefSessionKey derives the storage session id coref state is filed
// under. Deliberately distinct from the caller's own sessionID so this
// feature's one-row-per-session state never collides with (or is scanned
// alongside) unrelated MemoryItem rows the caller stores under the same
// conversational session.
func corefSessionKey(sessionID string) string {
	return sessionID + "::coref-subject"
}

// HasUnresolvedPronoun is the cheap regex-level anaphora check.
//
// Trips when query contains one of the tracked pronouns and no local
// antecedent (a capitalised, non-sentence-initial word) is already present
// in the same query — e.g. "Where is it located?" trips, but "What did
// Marie Curie discover and how did it change physics?" does not, because
// "Marie Curie" is a local antecedent for "it".
func HasUnresolvedPronoun(query string) bool {
	if !corefPronounRe.MatchString(query) {
		return false
	}
	words := strings.Fields(query)
	for _, word := range words[1:] { // skip the sentence-initial word
		stripped := strings.Trim(word, ".,!?;:'\"")
		if stripped == "" || len(stripped) <= 1 {
			continue
		}
		first := rune(stripped[0])
		lower := strings.ToLower(stripped)
		if first >= 'A' && first <= 'Z' && !corefPronouns[lower] && !corefNotAntecedents[lower] {
			return false
		}
	}
	return true
}

// corefRewriteWithSubject is pure string substitution: replace the first
// unresolved pronoun with subject. This is the "simple case" from the
// mechanism — an LLM call given only {lastSubject, newQuery} is a
// documented alternative for ambiguous phrasing but is not implemented here
// (out of scope for the minimal fix; nothing about this contract prevents
// adding it later).
func corefRewriteWithSubject(query, subject string) string {
	loc := corefPronounRe.FindStringIndex(query)
	if loc == nil {
		return query
	}
	return query[:loc[0]] + subject + query[loc[1]:]
}

// SubjectFromHit extracts the resolved-subject candidate from a winning
// POST /rag/query hit.
//
// Per the mechanism (#4530 step 1): the hit's first EntityIDs entry
// (already in #4514's frozen response contract — no new field needed)
// becomes the anchor subject for anaphora resolution on the next turn.
// Returns ("", false) when the hit carries no EntityIDs — nothing to
// remember, and the caller should skip CorefResolver.RememberSubject.
func SubjectFromHit(hit RagHit) (string, bool) {
	if len(hit.EntityIDs) == 0 {
		return "", false
	}
	return hit.EntityIDs[0], true
}

// CorefResolver is session-scoped anaphora resolution over a Memory client.
//
// Two calls drive the whole lifecycle:
//
//   - RememberSubject — call after a turn resolves a subject.
//   - Resolve — call before the next turn's /rag/query call; rewrites an
//     unresolved pronoun using the last remembered subject, or returns the
//     query unchanged when nothing trips.
//
// Single-turn callers pay zero extra cost: no sessionID or a query with no
// unresolved pronoun both short-circuit before any network call — the
// regex check in HasUnresolvedPronoun is the only always-on cost.
type CorefResolver struct {
	memory *Memory
}

// NewCorefResolver constructs a CorefResolver over the given Memory client.
func NewCorefResolver(memory *Memory) *CorefResolver {
	return &CorefResolver{memory: memory}
}

// RememberSubject persists subject as the resolved anchor for sessionID.
//
// Structurally one row: looks up any existing coref-state row for this
// session and supersedes it via consolidate (Memory.Update) instead of
// adding a second row, so state never grows with turn count.
func (r *CorefResolver) RememberSubject(ctx context.Context, sessionID, subject string) error {
	if sessionID == "" || subject == "" {
		return nil
	}
	storageSession := corefSessionKey(sessionID)
	existing, err := r.memory.Search(ctx, subject, WithSessionID(storageSession), WithTopK(1))
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		id, _ := existing[0]["id"].(string)
		_, err := r.memory.Update(ctx, id, subject)
		return err
	}
	_, err = r.memory.Add(ctx, subject, WithSessionID(storageSession), WithMemoryClass(corefMemoryClass))
	return err
}

// Resolve rewrites query using the last subject remembered for sessionID.
//
// Returns query unchanged (no network call) when sessionID is empty or the
// anaphora check does not trip. When it trips, recalls the session's coref
// state (a single bounded top_k=1 lookup); if no subject was ever stored,
// query is returned unchanged rather than guessing.
func (r *CorefResolver) Resolve(ctx context.Context, query, sessionID string) (string, error) {
	if sessionID == "" || !HasUnresolvedPronoun(query) {
		return query, nil
	}
	storageSession := corefSessionKey(sessionID)
	rows, err := r.memory.Search(ctx, query, WithSessionID(storageSession), WithTopK(1))
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return query, nil
	}
	subject, _ := rows[0]["content"].(string)
	if subject == "" {
		return query, nil
	}
	return corefRewriteWithSubject(query, subject), nil
}
