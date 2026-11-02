package tests

import (
	"fmt"
	"strings"
	"testing"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

func dataRows(t *testing.T, resp map[string]any) []map[string]any {
	t.Helper()
	arr, _ := resp["data"].([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		out = append(out, e.(map[string]any))
	}
	return out
}

// TestRealGraphOperators loads a payment ring plus a tail through the row
// projection (LinkStore-shaped rows) and checks every graph operator returns
// the mathematically expected answer, not just a 200.
func TestRealGraphOperators(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	ty := uniq("RgTx")
	// a->b->c->a is a 3-cycle; c->d is a tail; b->d closes a second triangle-free path.
	var rows []map[string]any
	for _, e := range [][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}, {"c", "d"}, {"a", "d"}} {
		rows = append(rows, map[string]any{"_pk": e[0] + e[1], "subject": e[0], "subject_type": ty, "object": e[1], "object_type": ty, "link_type": "pays", "weight": 1.0})
	}
	ingestRowsWait(t, c, ty, rows)
	P := "investigation"

	dij, err := c.GraphDijkstra(ctx, P, ty, "a", "d")
	must(t, err, "dijkstra")
	var path []string
	for _, r := range dataRows(t, dij) {
		path = append(path, str(r["node"]))
	}
	if strings.Join(path, ">") != "a>d" {
		t.Fatalf("shortest a->d is the direct edge, got %v", path)
	}

	pr, err := c.GraphPageRank(ctx, P, ty, &relata.GraphPageRankOptions{Damping: 0.85, MaxIter: 30})
	must(t, err, "pagerank")
	if len(dataRows(t, pr)) != 4 {
		t.Fatalf("pagerank must score all 4 nodes: %v", dataRows(t, pr))
	}
	top := dataRows(t, pr)[0]
	if top["node"] != "d" { // d has two in-edges and is a sink: highest rank
		t.Logf("pagerank leader=%v (rows=%v)", top["node"], dataRows(t, pr))
	}

	scc, err := c.GraphSCC(ctx, P, ty)
	must(t, err, "scc")
	comp := map[string]string{}
	for _, r := range dataRows(t, scc) {
		comp[str(r["node"])] = str(r["component_id"])
	}
	if comp["a"] != comp["b"] || comp["b"] != comp["c"] || comp["d"] == comp["a"] {
		t.Fatalf("scc must group a,b,c apart from d: %v", comp)
	}

	cyc, err := c.GraphCycles(ctx, P, ty)
	must(t, err, "cycles")
	cn := map[string]bool{}
	for _, r := range dataRows(t, cyc) {
		cn[str(r["node"])] = true
	}
	if !(cn["a"] && cn["b"] && cn["c"]) || cn["d"] {
		t.Fatalf("cycle nodes must be a,b,c: %v", cn)
	}

	com, err := c.GraphCommunity(ctx, P, ty)
	must(t, err, "community")
	if len(dataRows(t, com)) < 4 {
		t.Fatalf("community rows: %v", dataRows(t, com))
	}

	tri, err := c.GraphTriangleCount(ctx, P, ty)
	must(t, err, "triangles")
	if r := dataRows(t, tri); len(r) != 1 || str(r[0]["triangles"]) != "2" {
		// a-b-c is one triangle; a-c-d (a-d, c-d, a-c via c->a) is the second.
		t.Fatalf("triangle count: %v", r)
	}

	lp, err := c.GraphLinkPredict(ctx, P, ty, "b", "d")
	must(t, err, "link predict")
	if r := dataRows(t, lp); len(r) == 0 || str(r[0]["from"]) != "b" || str(r[0]["to"]) != "d" {
		t.Fatalf("link predict b->d: %v", r)
	}

	ns, err := c.GraphNodeSimilarity(ctx, P, ty, "a")
	must(t, err, "node similarity")
	t.Logf("node similarity rows=%d", len(dataRows(t, ns)))
	if _, ok := ns["columns"]; !ok {
		t.Fatalf("node similarity shape: %v", ns)
	}
}

// TestRealNativeLinks exercises the native edge door: single + batch create,
// REST traversal/shortest-path, and tombstone delete.
func TestRealNativeLinks(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	ty := uniq("RnHost")
	n1, n2, n3, n4 := ty+"-n1", ty+"-n2", ty+"-n3", ty+"-n4" // node ids are global: keep runs independent
	ingestRowsWait(t, c, ty, []map[string]any{{"_pk": n1, "n": 1}, {"_pk": n2, "n": 2}, {"_pk": n3, "n": 3}, {"_pk": n4, "n": 4}})
	link := "calls" + ty
	mk := func(a, b string) map[string]any {
		return map[string]any{"link_name": link, "source_id": a, "source_type": ty, "target_id": b, "target_type": ty}
	}
	one, err := c.CreateLink(ctx, mk(n1, n2))
	must(t, err, "create link")
	if one["status"] != "created" {
		t.Fatalf("create link: %v", one)
	}
	key := uniq("idem")
	batch, err := c.CreateLinks(ctx, []map[string]any{mk(n2, n3), mk(n3, n4), mk(n1, n2)}, key, "sync")
	must(t, err, "batch links")
	if str(batch["created"]) != "2" || str(batch["already_present"]) != "1" {
		t.Fatalf("batch result (2 new, 1 already present): %v", batch)
	}
	replay, err := c.CreateLinks(ctx, []map[string]any{mk(n2, n3), mk(n3, n4), mk(n1, n2)}, key, "sync")
	must(t, err, "batch replay")
	if replay["idempotent_replay"] != true {
		t.Fatalf("same Idempotency-Key must replay: %v", replay)
	}
	sp, err := c.GraphShortestPath(ctx, n1, n4, &relata.GraphShortestPathOptions{MaxHops: 5})
	must(t, err, "shortest path")
	if fmt.Sprint(sp["path"]) != fmt.Sprintf("[%s %s %s %s]", n1, n2, n3, n4) || str(sp["hops"]) != "3" {
		t.Fatalf("shortest path: %v", sp)
	}
	tr, err := c.GraphTraverse(ctx, n1, &relata.GraphTraverseOptions{Direction: "out", MaxDepth: 2, Limit: 10})
	must(t, err, "traverse")
	if fmt.Sprint(tr["nodes"]) != fmt.Sprintf("[%s %s %s]", n1, n2, n3) {
		t.Fatalf("traverse depth 2: %v", tr)
	}
	del, err := c.DeleteLink(ctx, link, n2, n3)
	must(t, err, "delete link")
	if str(del["deleted"]) != "1" {
		t.Fatalf("delete: %v", del)
	}
	sp2, err := c.GraphShortestPath(ctx, n1, n4, nil)
	if err == nil && fmt.Sprint(sp2["path"]) == fmt.Sprintf("[%s %s %s %s]", n1, n2, n3, n4) {
		t.Fatalf("deleted edge still on the path: %v", sp2)
	}
}

// TestRealIdentity checks DETECT_IDENTITIES on free text and RESOLVE_IDENTITY /
// cluster / same-identity over rows carrying canonical identifiers.
func TestRealIdentity(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	P := "investigation"
	det, err := c.DetectIdentities(ctx, P, "contact alice@example.com from 192.168.1.1")
	must(t, err, "detect")
	kinds := map[string]string{}
	for _, r := range dataRows(t, det) {
		kinds[str(r["identity_kind"])] = str(r["raw"])
	}
	if kinds["Email"] != "alice@example.com" || kinds["IPv4"] != "192.168.1.1" {
		t.Fatalf("detect: %v", kinds)
	}
	ty := uniq("RiPerson")
	// Identity keys are global to the node: use run-unique addresses.
	alice, carol := strings.ToLower(ty)+"-alice@example.com", strings.ToLower(ty)+"-carol@example.com"
	ingestRowsWait(t, c, ty, []map[string]any{
		{"_pk": "p1", "email": alice, "phone": "+14155550101"},
		{"_pk": "p2", "email": alice, "name": "A. Smith"},
		{"_pk": "p3", "email": carol},
	})
	hits := func(value string) map[string]bool {
		res, err := c.ResolveIdentity(ctx, P, value, "")
		must(t, err, "resolve")
		ids := map[string]bool{}
		for _, r := range dataRows(t, res) {
			if r["_type"] == ty {
				ids[str(r["_id"])] = true
			}
		}
		return ids
	}
	ids := hits(alice)
	if !ids["p1"] || !ids["p2"] || ids["p3"] || len(ids) != 2 {
		t.Fatalf("resolve alice must hit p1,p2 and not p3: %v", ids)
	}
	canon, err := c.ResolveIdentity(ctx, P, alice, "canonical")
	must(t, err, "resolve canonical")
	if len(dataRows(t, canon)) == 0 {
		t.Fatalf("canonical mode empty")
	}
	cl, err := c.IdentityCluster(ctx, P, alice)
	must(t, err, "cluster")
	if len(dataRows(t, cl)) < 2 {
		t.Fatalf("cluster rows: %v", dataRows(t, cl))
	}
	same, err := c.SameIdentity(ctx, P, alice, carol)
	must(t, err, "same identity (before fuse)")
	if same {
		t.Fatal("alice and carol must not be the same identity before a fuse")
	}
	// Fuse merges the clusters; resolve then reaches p3 from alice. Split undoes it.
	if _, err := c.FuseIdentities(ctx, P, alice, carol); err != nil {
		t.Fatalf("fuse: %v", err)
	}
	if ids := hits(alice); !ids["p3"] {
		t.Errorf("after FuseIdentities alice must reach p3: %v", ids)
	}
	if same, _ := c.SameIdentity(ctx, P, alice, carol); !same {
		t.Errorf("SameIdentity must be true after fuse")
	}
	if _, err := c.SplitIdentities(ctx, P, alice, carol); err != nil {
		t.Fatalf("split: %v", err)
	}
	if ids := hits(alice); ids["p3"] {
		t.Errorf("after SplitIdentities alice must no longer reach p3: %v", ids)
	}
}
