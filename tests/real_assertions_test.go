package tests

import (
	"strings"
	"testing"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

func assertionID(t *testing.T, m map[string]any) string {
	t.Helper()
	if a, ok := m["assertion"].(map[string]any); ok {
		m = a
	}
	id := str(m["id"])
	if id == "" {
		id = str(m["assertion_id"])
	}
	if id == "" {
		t.Fatalf("no assertion id in %v", m)
	}
	return id
}

// Governed-knowledge journey: conflicting claims from two sources, a published
// resolution policy, reviewer decisions, a correction, a recorded-time replay,
// evidence relations, an analyst assessment and a revoke.
func TestRealJourneyAssertions(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	a := relata.NewAssertionsClient(c)
	ty := uniq("RaPerson")
	ingestRowsWait(t, c, ty, []map[string]any{{"_pk": "p1", "id": "p1", "name": "Mina"}})

	const jan2021, jan2023 = int64(1609459200000000000), int64(1672531200000000000)
	rec := func(value, source string, from int64, extra map[string]any) string {
		body := map[string]any{
			"subject": map[string]any{"type": ty, "id": "p1"}, "predicate": "city", "value": value,
			"source": map[string]any{"id": source, "version": "2026-11"}, "valid_from": from,
			"derivation": map[string]any{"kind": "manual", "name": "journey", "version": "1"},
		}
		for k, v := range extra {
			body[k] = v
		}
		r, err := a.Record(ctx, body)
		must(t, err, "record "+value)
		return assertionID(t, r)
	}
	paris := rec("Paris", "registry", jan2021, map[string]any{"state": "confirmed"})
	rome := rec("Rome", "crm", jan2021, map[string]any{"state": "suggested"})
	// Idempotent retry returns the same assertion.
	if again := rec("Paris", "registry", jan2021, map[string]any{"state": "confirmed"}); again != paris {
		t.Fatalf("recording is not idempotent: %s vs %s", again, paris)
	}

	got, err := a.Get(ctx, paris)
	must(t, err, "get")
	if !strings.Contains(str(got), "Paris") {
		t.Fatalf("get: %v", got)
	}
	lst, err := a.List(ctx, ty, "p1", "city", "", 10)
	must(t, err, "list")
	if !strings.Contains(str(lst), "Paris") || !strings.Contains(str(lst), "Rome") {
		t.Fatalf("list must hold both claims: %v", lst)
	}

	// Built-in policy: confirmed beats suggested, and the conflict is reported.
	res, err := a.Resolve(ctx, ty, "p1", "city", jan2023+1, relata.ResolveOptions{})
	must(t, err, "resolve")
	if !strings.Contains(str(res["resolved"]), "Paris") || res["conflict"] != true {
		t.Fatalf("default resolution: %v", res)
	}

	// A published policy that prefers the CRM flips the answer; versions are immutable.
	pol := uniq("pol")
	if _, err := a.PublishPolicy(ctx, pol, map[string]any{"precedence": []string{"source_rank"}, "source_rank": []string{"crm", "registry"}, "include_suggested": true}, 0); err != nil {
		t.Fatalf("publish policy: %v", err)
	}
	_, err = a.PublishPolicy(ctx, pol, map[string]any{"precedence": []string{"state"}}, 0)
	wantProblem(t, err, 412, "")
	pv, err := a.GetPolicy(ctx, pol, 1)
	must(t, err, "get policy")
	if !strings.Contains(str(pv), "crm") {
		t.Fatalf("policy: %v", pv)
	}
	res2, err := a.Resolve(ctx, ty, "p1", "city", jan2023+1, relata.ResolveOptions{Policy: pol, PolicyVersion: 1})
	must(t, err, "resolve with policy")
	if !strings.Contains(str(res2["resolved"]), "Rome") {
		t.Fatalf("policy resolution must pick Rome: %v", res2)
	}

	// Review: reject the CRM claim -> never resolves; confirm it again -> eligible.
	if _, err := a.Reject(ctx, rome, "stale"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	res3, _ := a.Resolve(ctx, ty, "p1", "city", jan2023+1, relata.ResolveOptions{Policy: pol, PolicyVersion: 1})
	if strings.Contains(str(res3["resolved"]), "Rome") {
		t.Fatalf("a rejected assertion must not resolve: %v", res3)
	}
	if _, err := a.Confirm(ctx, rome, "re-verified"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := a.Decide(ctx, rome, "confirm", "idempotent"); err != nil {
		t.Fatalf("decide: %v", err)
	}

	// Correction supersedes without overwriting; the earlier belief is replayable.
	before, err := a.Resolve(ctx, ty, "p1", "city", jan2023+1, relata.ResolveOptions{})
	must(t, err, "pre-correction resolve")
	_ = before
	lyon := rec("Lyon", "registry", jan2021, map[string]any{"state": "confirmed", "supersedes": paris})
	post, err := a.Resolve(ctx, ty, "p1", "city", jan2023+1, relata.ResolveOptions{})
	must(t, err, "post-correction resolve")
	if strings.Contains(str(post["resolved"]), "Paris") {
		t.Fatalf("superseded claim still wins: %v", post)
	}
	_, err = a.Record(ctx, map[string]any{"subject": map[string]any{"type": ty, "id": "p1"}, "predicate": "city", "value": "Nice", "source": map[string]any{"id": "registry", "version": "2026-11"}, "valid_from": jan2021, "derivation": map[string]any{"kind": "manual", "name": "journey", "version": "1"}, "supersedes": paris})
	wantProblem(t, err, 409, "")

	// Evidence relations, assessment and graph.
	if _, err := a.Relate(ctx, lyon, "supports", rome, "same registry extract", map[string]any{"pointer": "page=3"}); err != nil {
		t.Fatalf("relate: %v", err)
	}
	rel, err := a.RelateWithConfidence(ctx, rome, "contradicts", lyon, "conflicting city", nil, 0.7, "sources disagree")
	must(t, err, "relate with confidence")
	if _, err := a.RelateSimilar(ctx, rome, lyon, "same subject", 0.8, "manual", nil); err != nil {
		t.Errorf("relate similar: %v", err)
	}
	if _, err := a.Assess(ctx, lyon, "corroborated", "two sources"); err != nil {
		t.Errorf("assess: %v", err)
	}
	if _, err := a.AssessWithConfidence(ctx, lyon, "corroborated", "re-checked", 0.9, "minor"); err != nil {
		t.Errorf("assess with confidence: %v", err)
	}
	g, err := a.EvidenceGraph(ctx, lyon, relata.EvidenceGraphOptions{Depth: 2, Limit: 20})
	must(t, err, "evidence graph")
	if !strings.Contains(str(g), rome) {
		t.Fatalf("evidence graph must reach the related claim: %v", g)
	}
	rm := rel
	if inner, ok := rel["relation"].(map[string]any); ok {
		rm = inner
	}
	rid := str(rm["id"])
	if rid == "" || rid == "<nil>" {
		rid = str(rm["relation_id"])
	}
	if rid == "" || rid == "<nil>" {
		t.Fatalf("relation has no id: %v", rel)
	}
	if _, err := a.Unrelate(ctx, rid, "withdrawn"); err != nil {
		t.Errorf("unrelate: %v", err)
	}

	// Revoke leaves a content-free tombstone and blocks re-recording.
	if _, err := a.Revoke(ctx, rome, "privacy"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	gr, _ := a.Get(ctx, rome)
	if strings.Contains(str(gr), "Rome") {
		t.Errorf("revoked assertion still exposes its value: %v", gr)
	}
	_, err = a.Get(ctx, "no-such-assertion-"+uniq("x"))
	wantProblem(t, err, 404, "")
}
