package tests

import (
	"strings"
	"testing"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// TestRealTypeLifecycle drives type registration, introspection, online alter
// (with compare-and-set), edge/detect config, branches and deregistration with
// a schema-admin registry token, and proves a plain data token is refused.
func TestRealTypeLifecycle(t *testing.T) {
	sa := roleClient(t, "schema-admin")
	data := realClient(t)
	ctx := ctxT(t)
	ty := uniq("RtSchema")

	reg, err := sa.RegisterType(ctx, ty, map[string]any{
		"description": "sdk-real lifecycle", "owner": "sdk-real",
		"properties": []map[string]any{{"name": "email", "required": true}},
	})
	must(t, err, "register")
	if reg["registered"] != true || reg["name"] != ty {
		t.Fatalf("register response: %v", reg)
	}
	// A data token must not be able to administer schema.
	_, err = data.RegisterType(ctx, ty+"X", nil)
	if re := wantProblemAny(t, err); re.StatusCode != 401 && re.StatusCode != 403 {
		t.Fatalf("data token create: %d", re.StatusCode)
	}

	detail, err := data.TypeDetail(ctx, ty)
	must(t, err, "detail")
	v0, _ := detail["schema_version"].(string)
	if detail["name"] != ty || len(v0) != 16 {
		t.Fatalf("detail: %v", detail)
	}

	// ListTypes + QuerySchema both see the new type.
	lt, err := data.ListTypes(ctx)
	must(t, err, "list types")
	found := false
	for _, e := range lt["types"].([]any) {
		if e.(map[string]any)["name"] == ty {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListTypes misses %s", ty)
	}
	qs, err := data.QuerySchema(ctx, "investigation")
	must(t, err, "query schema")
	if !containsAny(qs["object_types"], ty) {
		t.Fatalf("QuerySchema misses %s: %v", ty, qs["object_types"])
	}

	// Alter: add -> new schema_version; stale CAS -> 412; fresh CAS -> ok.
	al, err := sa.SchemaAlter(ctx, ty, "add", "age", &relata.SchemaAlterOptions{ColType: "int"})
	must(t, err, "alter add")
	v1, _ := al["schema_version"].(string)
	if al["action"] != "add_column" || v1 == v0 || al["schema_version_before"] != v0 {
		t.Fatalf("alter add: %v (v0=%s)", al, v0)
	}
	_, err = sa.SchemaAlter(ctx, ty, "add", "city", &relata.SchemaAlterOptions{ColType: "text", ExpectedSchemaVersion: v0})
	wantProblem(t, err, 412, "")
	al2, err := sa.SchemaAlter(ctx, ty, "add", "city", &relata.SchemaAlterOptions{ColType: "text", ExpectedSchemaVersion: v1})
	must(t, err, "alter cas")
	if al2["schema_version_before"] != v1 {
		t.Fatalf("CAS alter: %v", al2)
	}
	if _, err := sa.SchemaAlter(ctx, ty, "rename", "city", &relata.SchemaAlterOptions{NewColumn: "town"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := sa.SchemaAlter(ctx, ty, "drop", "town", nil); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := sa.SchemaAlter(ctx, ty, "retype", "age", &relata.SchemaAlterOptions{ColType: "text"}); err != nil {
		t.Fatalf("retype: %v", err)
	}
	wantProblem(t, func() error {
		_, e := sa.SchemaAlter(ctx, "NoSuchType"+ty, "add", "x", &relata.SchemaAlterOptions{ColType: "text"})
		return e
	}(), 404, "type-not-found")

	lc, err := data.TypeLifecycle(ctx, ty)
	must(t, err, "lifecycle")
	if lc["name"] != ty || lc["exists"] != true {
		t.Fatalf("lifecycle: %v", lc)
	}

	// Edge declaration + listing.
	_, err = sa.RegisterEdgeType(ctx, ty, ty, "reports_to")
	must(t, err, "edge type")
	et, err := data.ListEdgeTypes(ctx)
	must(t, err, "list edges")
	var edgeSeen bool
	for _, e := range et["edges"].([]any) {
		m := e.(map[string]any)
		if m["from_type"] == ty && m["to_type"] == ty {
			edgeSeen = true
			if m["relation"] != "reports_to" {
				t.Errorf("registered label lost: edge=%v (SDK sends label, server stores relation)", m)
			}
		}
	}
	if !edgeSeen {
		t.Fatalf("edge not listed: %v", et)
	}

	// Detector packs.
	dc, err := sa.SetTypeDetectConfig(ctx, ty, []string{"none"})
	must(t, err, "detect cfg")
	if dc["configured"] != true || dc["type"] != ty {
		t.Fatalf("detect cfg: %v", dc)
	}
	ldc, err := data.ListTypeDetectConfigs(ctx)
	must(t, err, "list detect")
	if !strings.Contains(str(ldc["configs"]), ty) {
		t.Fatalf("detect config not listed: %v", ldc)
	}

	// Enrichment rules + ontology migrate (admin).
	er, err := sa.EnrichmentRules(ctx, map[string]any{"source_type": ty, "match_field": "email", "enrich_fields": []string{"name"}})
	must(t, err, "enrichment")
	if er["registered"] != true {
		t.Fatalf("enrichment: %v", er)
	}
	om, err := sa.OntologyMigrate(ctx, map[string]any{})
	must(t, err, "ontology migrate")
	if _, ok := om["applied"]; !ok {
		t.Fatalf("ontology migrate response: %v", om)
	}

	// Schema branches.
	br := uniq("rtbr")
	cb, err := sa.CreateSchemaBranch(ctx, br, "main")
	must(t, err, "create branch")
	if cb["created"] != true || cb["branch"] != br {
		t.Fatalf("branch: %v", cb)
	}
	// A single-node server refuses branch deletion with a typed problem
	// ("distributed retention coordination unavailable"); the SDK must surface it
	// as a RelataError rather than swallow it.
	_, err = sa.DeleteSchemaBranch(ctx, br)
	if err != nil {
		re := wantProblemAny(t, err)
		if !strings.Contains(re.Message, "branch deletion refused") {
			t.Fatalf("branch delete problem: %v", re)
		}
	}

	// Deregister the empty type.
	dr, err := sa.DeregisterType(ctx, ty)
	must(t, err, "deregister")
	if dr["deregistered"] != true {
		t.Fatalf("deregister: %v", dr)
	}
	wantProblem(t, func() error { _, e := data.TypeDetail(ctx, ty); return e }(), 404, "")
}

func containsAny(v any, want string) bool {
	arr, _ := v.([]any)
	for _, e := range arr {
		if s, _ := e.(string); s == want {
			return true
		}
	}
	return false
}
