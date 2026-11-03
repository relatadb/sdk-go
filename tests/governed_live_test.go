package tests

/**
 * Governed-knowledge run through the Go SDK against a live server (#5711,
 * #5712, #5721): ontology definition publish with a stale-publish 412, stamped
 * rows, reversible entity-resolution merges, and evidence relations.
 * Mirrors scripts/sdk_governed_e2e.py. Skips unless RELATA_TOKEN is set.
 * Run: scripts/sdk_governed_live.sh (needs a running server).
 */

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

func rawJSON(t *testing.T, method, host, token, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, host+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func TestGovernedLive(t *testing.T) {
	token := os.Getenv("RELATA_TOKEN")
	if token == "" {
		t.Skip("RELATA_TOKEN not set; live governed run skipped")
	}
	host := getEnv("RELATA_HOST", "http://localhost:9090")
	ctx := context.Background()
	c := relata.New(host, &relata.ClientOptions{BearerToken: token, Timeout: 30 * time.Second})
	uid := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	ty, name := "GoGovPerson"+uid, "crm_go_"+uid

	// ---- #5711
	od := relata.NewOntologyDefinitionsClient(c)
	def := map[string]any{"objects": []any{map[string]any{"name": ty, "properties": []any{
		map[string]any{"name": "email", "type": "text", "required": true}}}}, "links": []any{}}
	if _, err := od.Create(ctx, name, def); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := od.Compare(ctx, name, "", ""); err != nil {
		t.Fatalf("compare: %v", err)
	}
	if _, err := od.Publish(ctx, name, relata.PublishOptions{ExpectedVersion: 0}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := od.Publish(ctx, name, relata.PublishOptions{ExpectedVersion: 0}); err == nil {
		t.Fatal("a stale publish must be refused")
	} else if s := statusOf(err); s != 412 {
		t.Fatalf("stale publish status %d (%v), want 412", s, err)
	}
	s, b := rawJSON(t, "POST", host, token, "/v1/ingest?purpose=investigation&object_type="+ty, []any{
		map[string]any{"id": "p1", "email": "ada@example.com"}, map[string]any{"id": "p2", "email": "ADA@example.com"}})
	if s != 200 {
		t.Fatalf("ingest: %d %v", s, b)
	}
	stamped := false
	for i := 0; i < 100 && !stamped; i++ {
		st, err := od.Status(ctx, name)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		types, _ := st["types"].([]any)
		if len(types) > 0 {
			bv, _ := types[0].(map[string]any)["by_version"].(map[string]any)
			stamped = bv["1"] == float64(2)
		}
		if !stamped {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if !stamped {
		t.Fatal("ingested rows were not stamped with definition version 1")
	}
	res, err := c.Search(ctx, "ada", ty)
	if err != nil || len(res.Hits) == 0 {
		t.Fatalf("search: %v %+v", err, res)
	}
	for _, h := range res.Hits {
		if h.Definition["current"] != name+"@1" {
			t.Fatalf("hit lacks its definition version: %+v", h)
		}
	}

	// ---- #5712
	rc := relata.NewResolutionClient(c)
	rs := "go_people_" + uid
	if _, err := rc.PublishRuleset(ctx, rs, map[string]any{
		"entity_type": ty, "confidence_meaning": "test score, not calibrated",
		"rules": []any{map[string]any{"id": "email", "kind": "exact", "fields": []any{"email"},
			"normalize": []any{"trim", "lowercase"}, "weight": 0.95, "strength": "strong"}},
		"thresholds":          map[string]any{"auto_merge": 0.9, "review": 0.5},
		"approved_thresholds": map[string]any{"min_precision": 0.5, "min_recall": 0.5}}, 0); err != nil {
		t.Fatalf("publish ruleset: %v", err)
	}
	if _, err := rc.Run(ctx, rs, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	cands, err := rc.ListCandidates(ctx, rs, "", 0)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	items, _ := cands["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want one candidate, got %v", cands)
	}
	cid, _ := items[0].(map[string]any)["id"].(string)
	if _, err := rc.Approve(ctx, cid, "go sdk"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	look := func() any {
		l, err := rc.Lookup(ctx, ty, "p2")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		return l["clustered"]
	}
	if look() != true {
		t.Fatal("p2 should be merged")
	}
	for i := 0; i < 2; i++ { // second unmerge is idempotent
		if _, err := rc.Unmerge(ctx, cid, "wrong person"); err != nil {
			t.Fatalf("unmerge #%d: %v", i, err)
		}
	}
	if look() != false {
		t.Fatal("unmerge must reverse the merge")
	}

	// ---- #5721
	ac := relata.NewAssertionsClient(c)
	rec := func(value, src string) string {
		a, err := ac.Record(ctx, map[string]any{"subject": map[string]any{"type": ty, "id": "p1"},
			"predicate": "city", "value": value, "source": map[string]any{"id": src + "-" + uid, "version": "1"},
			"valid_from": 100, "derivation": map[string]any{"kind": "manual", "name": "x", "version": "1"}})
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		if inner, ok := a["assertion"].(map[string]any); ok {
			a = inner
		}
		return a["id"].(string)
	}
	a1, a2 := rec("Oslo", "s1"), rec("Bergen", "s2")
	rel, err := ac.Relate(ctx, a1, "contradicts", a2, "different cities", map[string]any{"pointer": "fixture=1"})
	if err != nil {
		t.Fatalf("relate: %v", err)
	}
	if _, err := ac.AssessWithConfidence(ctx, a1, "disputed", "go sdk", 0.4, "source age unknown"); err != nil {
		t.Fatalf("assess: %v", err)
	}
	g, err := ac.EvidenceGraph(ctx, a1, relata.EvidenceGraphOptions{Depth: 2, Limit: 20})
	if err != nil || !strings.Contains(fmt.Sprint(g), "contradicts") {
		t.Fatalf("evidence graph: %v %v", err, g)
	}
	if inner, ok := rel["relation"].(map[string]any); ok {
		rel = inner
	}
	if _, err := ac.Unrelate(ctx, rel["id"].(string), "withdrawn"); err != nil {
		t.Fatalf("unrelate: %v", err)
	}
	after, err := ac.EvidenceGraph(ctx, a1, relata.EvidenceGraphOptions{Depth: 2, Limit: 20})
	if rels, _ := after["relations"].([]any); err != nil || len(rels) != 0 {
		t.Fatalf("unrelate must withdraw the relation: %v %v", err, after)
	}
}
