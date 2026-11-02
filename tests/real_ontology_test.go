package tests

import (
	"strings"
	"testing"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// Ontology journey: versioned definitions (validate, publish, compare,
// refuse an unsafe change, publish a migration), then the extraction review
// queue (submit, low-confidence guard, accept/correct/reject/reopen, apply
// into the draft) and the ETL existence lookup.
func TestRealJourneyOntologyAndExtraction(t *testing.T) {
	admin := roleClient(t, "schema-admin")
	ctx := ctxT(t)
	od := relata.NewOntologyDefinitionsClient(admin)
	name := uniq("onto")
	obj := "Person" + name
	v1 := map[string]any{"objects": []map[string]any{{"name": obj, "properties": []map[string]any{{"name": "email", "type": "text"}, {"name": "age", "type": "int"}}}}, "links": []any{}}
	if _, err := od.Create(ctx, name, v1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if v, err := od.Validate(ctx, name, nil); err != nil {
		t.Fatalf("validate: %v", err)
	} else {
		t.Logf("validate: %v", v)
	}
	bad := map[string]any{"objects": []map[string]any{{"name": obj, "properties": []map[string]any{{"name": "x", "type": "nonsense"}}}}, "links": []any{}}
	vb, err := od.Validate(ctx, name, bad)
	must(t, err, "validate bad")
	if vb["valid"] != false || !strings.Contains(str(vb), "issues") {
		t.Fatalf("an unknown property type must be reported invalid: %v", vb)
	}
	if _, err := od.Publish(ctx, name, relata.PublishOptions{ExpectedVersion: 0}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	_, err = od.Publish(ctx, name, relata.PublishOptions{ExpectedVersion: 0})
	wantProblem(t, err, 412, "")
	got, err := od.Get(ctx, name)
	must(t, err, "get")
	if !strings.Contains(str(got), "ontology_version") || got["published_version"] != float64(1) {
		t.Fatalf("get: %v", got)
	}
	all, err := od.List(ctx)
	must(t, err, "list")
	if !strings.Contains(str(all), name) {
		t.Fatalf("list lacks %s: %v", name, all)
	}
	ver, err := od.Version(ctx, name, 1)
	must(t, err, "version 1")
	if !strings.Contains(str(ver), "email") || !strings.Contains(str(ver), "sha256") {
		t.Errorf("published version must carry a content hash: %v", ver)
	}

	// Draft: add an optional property (compatible) then drop age (unsafe).
	v2 := map[string]any{"objects": []map[string]any{{"name": obj, "properties": []map[string]any{{"name": "email", "type": "text"}, {"name": "age", "type": "int"}, {"name": "city", "type": "text"}}}}, "links": []any{}}
	if _, err := od.SaveDraft(ctx, name, v2); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	cmp, err := od.Compare(ctx, name, "1", "draft")
	must(t, err, "compare")
	if !strings.Contains(str(cmp), "compatible") {
		t.Errorf("adding an optional property is compatible: %v", cmp)
	}
	if _, err := od.Publish(ctx, name, relata.PublishOptions{ExpectedVersion: 1}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	v3 := map[string]any{"objects": []map[string]any{{"name": obj, "properties": []map[string]any{{"name": "email", "type": "text"}, {"name": "city", "type": "text"}}}}, "links": []any{}}
	if _, err := od.SaveDraft(ctx, name, v3); err != nil {
		t.Fatalf("save draft 3: %v", err)
	}
	_, err = od.Publish(ctx, name, relata.PublishOptions{ExpectedVersion: 2})
	if err == nil {
		t.Fatal("dropping a property must not publish without consent")
	}
	wantProblemAny(t, err)
	if _, err := od.Publish(ctx, name, relata.PublishOptions{ExpectedVersion: 2, AcceptMigration: true, OverrideUnsafe: true}); err != nil {
		t.Fatalf("publish v3 with consent: %v", err)
	}
	st, err := od.Status(ctx, name)
	must(t, err, "status")
	t.Logf("status: %v", st)
	if mg, err := od.Migration(ctx, name, 3); err != nil {
		t.Logf("migration read: %v", err)
	} else {
		t.Logf("migration: %v", mg)
		if rm, err := od.RunMigration(ctx, name, 3, 100, false); err != nil {
			t.Logf("run migration: %v", err)
		} else {
			t.Logf("run migration: %v", rm)
		}
	}

	// Extraction review queue.
	ex := relata.NewExtractionClient(admin)
	src := uniq("doc")
	mk := func(kind string, conf float64, extra map[string]any) map[string]any {
		s := map[string]any{
			"kind": kind, "definition": name,
			"source":     map[string]any{"id": src, "version": "1"},
			"extractor":  map[string]any{"name": "ner", "version": "1", "confidence_meaning": "calibrated"},
			"confidence": conf, "evidence": map[string]any{"locator": src + "#p1", "excerpt": "Ada joined"},
		}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	res, err := ex.Submit(ctx, []map[string]any{
		mk("entity", 0.93, map[string]any{"payload": map[string]any{"type": obj, "properties": map[string]any{"email": "ada@example.com"}}}),
		mk("entity", 0.3, map[string]any{"payload": map[string]any{"type": obj, "properties": map[string]any{"email": "low@example.com"}}}),
		mk("property", 0.9, map[string]any{"payload": map[string]any{"object": obj, "property": map[string]any{"name": "country", "type": "text"}}}),
		{"kind": "entity", "state": "accepted", "payload": map[string]any{"type": obj, "properties": map[string]any{}}}, // may not self-promote
	})
	must(t, err, "submit")
	results, _ := res["results"].([]any)
	if len(results) != 4 {
		t.Fatalf("submit results: %v", res)
	}
	status := func(i int) string { return str(results[i].(map[string]any)["status"]) }
	if status(0) != "created" || status(1) != "created" || status(2) != "created" || status(3) != "invalid" {
		t.Fatalf("submit statuses %s/%s/%s: %v", status(0), status(1), status(3), results)
	}
	idOf := func(i int) string {
		m := results[i].(map[string]any)
		id := str(m["id"])
		if id == "" || id == "<nil>" {
			id = str(m["suggestion_id"])
		}
		return id
	}
	again, err := ex.Submit(ctx, []map[string]any{mk("entity", 0.93, map[string]any{"payload": map[string]any{"type": obj, "properties": map[string]any{"email": "ada@example.com"}}})})
	must(t, err, "resubmit")
	if !strings.Contains(str(again), "existing") {
		t.Errorf("an exact retry must be reported as existing: %v", again)
	}
	q, err := ex.List(ctx, relata.SuggestionFilter{State: "pending", Definition: name, Limit: 10})
	must(t, err, "list suggestions")
	if !strings.Contains(str(q), "ada@example.com") {
		t.Fatalf("queue: %v", q)
	}
	g, err := ex.Get(ctx, idOf(0))
	must(t, err, "get suggestion")
	if !strings.Contains(str(g), "pending") {
		t.Errorf("new suggestion must start pending: %v", g)
	}
	_, err = ex.Accept(ctx, idOf(1), "looks fine", false)
	wantProblem(t, err, 409, "")
	if _, err := ex.Accept(ctx, idOf(1), "verified by hand", true); err != nil {
		t.Errorf("accept low confidence with consent: %v", err)
	}
	if _, err := ex.Accept(ctx, idOf(0), "matches the document", false); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := ex.Correct(ctx, idOf(0), map[string]any{"type": obj, "properties": map[string]any{"email": "ada.l@example.com"}}, "typo"); err != nil {
		t.Errorf("correct: %v", err)
	}
	if _, err := ex.Reject(ctx, idOf(0), "wrong person"); err != nil {
		t.Errorf("reject: %v", err)
	}
	if _, err := ex.Reopen(ctx, idOf(0), "second look"); err != nil {
		t.Errorf("reopen: %v", err)
	}
	if _, err := ex.Decide(ctx, idOf(2), "accept", "needed", nil, false); err != nil {
		t.Fatalf("decide schema suggestion: %v", err)
	}
	ap, err := ex.Apply(ctx, name, nil)
	must(t, err, "apply")
	if !strings.Contains(str(ap), "country") && !strings.Contains(str(ap), "applied") {
		t.Errorf("apply: %v", ap)
	}
	// ETL existence lookup against real rows.
	ingestRowsWait(t, admin, obj, []map[string]any{{"_pk": "e1", "id": "e1", "email": "known-" + name + "@example.com"}})
	ee, err := ex.EntitiesExist(ctx, []map[string]any{
		{"type": obj, "match": map[string]any{"email": "known-" + name + "@example.com"}},
		{"type": obj, "match": map[string]any{"email": "nobody-" + name + "@example.com"}},
	}, name)
	must(t, err, "entities exist")
	rs, _ := ee["results"].([]any)
	if len(rs) != 2 || rs[0].(map[string]any)["exists"] != true || rs[1].(map[string]any)["exists"] != false {
		t.Errorf("entities exist: %v", ee)
	}
}
