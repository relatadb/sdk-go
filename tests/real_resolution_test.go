package tests

import (
	"strings"
	"testing"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// Entity-resolution journey: publish a rule set, run it over real records,
// inspect the explainable candidate, evaluate against labelled pairs, then
// approve, look up the canonical entity, unmerge and reject.
func TestRealJourneyEntityResolution(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	r := relata.NewResolutionClient(c)
	ty := uniq("ErPerson")
	tag := strings.ToLower(ty)
	ingestRowsWait(t, c, ty, []map[string]any{
		{"_pk": "r1", "id": "r1", "email": tag + "@example.com", "name": "Mina Qadri", "source": "crm"},
		{"_pk": "r2", "id": "r2", "email": strings.ToUpper(tag) + "@example.com ", "name": "M. Qadri", "source": "web"},
		{"_pk": "r3", "id": "r3", "email": "other-" + tag + "@example.com", "name": "Zed Other", "source": "crm"},
		{"_pk": "r4", "id": "r4", "email": "x-" + tag + "@example.com", "name": "Pat Four", "source": "crm"},
		{"_pk": "r5", "id": "r5", "email": "x-" + tag + "@example.com", "name": "Patricia Four", "source": "web"},
	})
	rs := uniq("rs")
	def := map[string]any{
		"entity_type": ty, "id_field": "id", "source_field": "source",
		"confidence_meaning":  "Probability-like score that two records describe the same person; not a calibrated guarantee.",
		"source_reliability":  map[string]any{"crm": 1.0, "web": 0.9},
		"rules":               []map[string]any{{"id": "email", "kind": "exact", "fields": []string{"email"}, "normalize": []string{"trim", "lowercase"}, "weight": 0.95, "strength": "strong", "max_group": 2}},
		"thresholds":          map[string]any{"auto_merge": 0.9, "review": 0.5},
		"policy":              map[string]any{"auto_merge": false},
		"approved_thresholds": map[string]any{"min_precision": 0.75, "min_recall": 0.75, "approved_by": "journey steward"},
	}
	if _, err := r.PublishRuleset(ctx, rs, def, 0); err != nil {
		t.Fatalf("publish ruleset: %v", err)
	}
	_, err := r.PublishRuleset(ctx, rs, def, 0)
	wantProblem(t, err, 412, "")
	got, err := r.GetRuleset(ctx, rs, 1)
	must(t, err, "get ruleset")
	if !strings.Contains(str(got), "email") {
		t.Fatalf("ruleset: %v", got)
	}
	all, err := r.ListRulesets(ctx)
	must(t, err, "list rulesets")
	if !strings.Contains(str(all), rs) {
		t.Fatalf("ruleset %s not listed: %v", rs, all)
	}
	run, err := r.Run(ctx, rs, nil)
	must(t, err, "run")
	t.Logf("run: %v", run)
	cands, err := r.ListCandidates(ctx, rs, "pending", 20)
	must(t, err, "list candidates")
	items, _ := cands["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("expected 2 candidate pairs (r1/r2, r4/r5): %v", cands)
	}
	var first, second string
	for _, it := range items {
		m := it.(map[string]any)
		s := str(m)
		switch {
		case strings.Contains(s, "r1") && strings.Contains(s, "r2"):
			first = str(m["id"])
		case strings.Contains(s, "r4") && strings.Contains(s, "r5"):
			second = str(m["id"])
		}
	}
	if first == "" || second == "" {
		t.Fatalf("candidate ids not found: %v", items)
	}
	cand, err := r.GetCandidate(ctx, first)
	must(t, err, "get candidate")
	if !strings.Contains(str(cand), "explanation") || !strings.Contains(str(cand), "fingerprint") {
		t.Fatalf("candidate must explain itself with per-rule fingerprints: %v", cand)
	}
	ev, err := r.Evaluate(ctx, rs, []map[string]any{{"a": "r1", "b": "r2", "same": true}, {"a": "r1", "b": "r3", "same": false}, {"a": "r4", "b": "r5", "same": true}}, nil)
	must(t, err, "evaluate")
	if ev["passes"] != true {
		t.Errorf("labelled pairs should pass the approved thresholds: %v", ev)
	}

	if _, err := r.Approve(ctx, first, "same person"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	lk, err := r.Lookup(ctx, ty, "r1")
	must(t, err, "lookup")
	if lk["clustered"] != true {
		t.Fatalf("r1 must be clustered after approve: %v", lk)
	}
	canon := str(lk["canonical"])
	if canon == "" || canon == "<nil>" {
		canon = str(lk["canonical_id"])
	}
	ent, err := r.Entity(ctx, canon)
	must(t, err, "entity")
	if !strings.Contains(str(ent), "r2") {
		t.Fatalf("entity must list both members: %v", ent)
	}
	again, err := r.Decide(ctx, first, "approve", "retry")
	must(t, err, "idempotent approve")
	if again["idempotent"] != true {
		t.Errorf("repeat approve must be idempotent: %v", again)
	}
	if _, err := r.Unmerge(ctx, first, "mistake"); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	lk2, _ := r.Lookup(ctx, ty, "r1")
	if lk2["clustered"] == true {
		t.Errorf("unmerge must restore the lone-record lookup: %v", lk2)
	}
	if _, err := r.Reject(ctx, second, "different people"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	_, err = r.Approve(ctx, second, "changed my mind")
	wantProblem(t, err, 409, "")
	_, err = r.GetCandidate(ctx, "no-such-candidate-"+uniq("c"))
	wantProblem(t, err, 404, "")
}
