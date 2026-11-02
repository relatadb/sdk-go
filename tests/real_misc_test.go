package tests

import (
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// Search options, query-builder helpers, identity governance, lookups, media
// work, sessions and the remaining SQL-door helpers against the live server.
func TestRealSearchOptionsAndBuilders(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	ty := uniq("RmDoc")
	tag := strings.ToLower(uniq("zorblax"))
	ingestRowsWait(t, c, ty, []map[string]any{
		{"_pk": "d1", "title": "Alpha " + tag + " report", "team": "red", "id": "d1"},
		{"_pk": "d2", "title": "Beta " + tag + " memo", "team": "blue", "id": "d2"},
		{"_pk": "d3", "title": "Gamma unrelated", "team": "red", "id": "d3"},
	})
	var res *relata.SearchResponse
	var err error
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		res, err = c.Search(ctx, tag, ty, relata.WithSearchLimit(5), relata.WithHighlight(), relata.WithSearchFacets("team"), relata.WithMatchingStrategy("all"), relata.WithTypoTolerance(map[string]any{"enabled": true}))
		must(t, err, "search")
		if len(res.Hits) >= 2 {
			break
		}
	}
	if len(res.Hits) != 2 {
		t.Fatalf("search for %q must find exactly the two matching documents: %+v", tag, res)
	}
	red, err := c.Search(ctx, tag, ty, relata.WithSearchFilter("team", "red"))
	must(t, err, "filtered search")
	if len(red.Hits) != 1 {
		t.Errorf("filter team=red must leave one hit, got %d", len(red.Hits))
	}
	if _, err := c.Search(ctx, tag, ty, relata.WithMetric("cosine"), relata.WithWeights(0, 0.5, 0.5)); err != nil {
		t.Errorf("hybrid search options: %v", err)
	}

	// Builder helpers produce executable SQL.
	hb, err := relata.HybridSearch(ty, tag, 3)
	must(t, err, "hybrid builder")
	hr, err := hb.Execute(ctx, c)
	must(t, err, "hybrid builder execute")
	if len(hr.Rows) == 0 {
		t.Errorf("hybrid builder returned no rows for a tag present in the corpus")
	}
	if r, err := relata.LookupIdentity(tag+"@example.com").Purpose("investigation").Execute(ctx, c); err != nil {
		t.Logf("lookup identity builder: %v", err)
	} else {
		t.Logf("lookup identity rows=%d", len(r.Rows))
	}
	if r, err := relata.PathsBetween("a", "b", 3).Purpose("investigation").Execute(ctx, c); err != nil {
		t.Logf("paths builder: %v", err)
	} else {
		t.Logf("paths rows=%d", len(r.Rows))
	}
	_, err = relata.MatchFace("blob://nope", 3).Purpose("investigation").Execute(ctx, c)
	wantProblemAny(t, err) // MATCH_FACE is retired server-side: typed parse error
	if _, err := c.SimilarImage(ctx, "blob://nope", relata.WithSimilarImageThreshold(0.8), relata.WithSimilarImageIndex("faces"), relata.WithSimilarImagePurpose("investigation")); err == nil {
		t.Logf("similar image returned without error")
	} else {
		wantProblemAny(t, err)
	}
	if r, err := c.ConvoyDetect(ctx, "investigation", &relata.ConvoyDetectOptions{RadiusM: 100, TimeTolSecs: 60, MinPoints: 3}); err != nil {
		t.Logf("convoy: %v", err)
	} else {
		t.Logf("convoy: %d keys", len(r))
	}
	if r, err := c.Geofence(ctx, "investigation", "poly:0,0;0,1;1,1;1,0", ty); err != nil {
		t.Logf("geofence: %v", err)
	} else {
		t.Logf("geofence: %d keys", len(r))
	}
	// Sessions: an unknown session holds no staged rows; every verb answers
	// with a zero count rather than inventing data.
	sid := "srgo-nosuch-" + strings.ToLower(uniq("s"))
	d, err := c.SessionDiff(ctx, sid)
	must(t, err, "session diff")
	if d["session_id"] != sid || d["staged_count"] != float64(0) {
		t.Errorf("diff of an unknown session: %v", d)
	}
	cm, err := c.SessionCommit(ctx, sid)
	must(t, err, "session commit")
	if cm["committed"] != float64(0) {
		t.Errorf("commit of an unknown session must commit nothing: %v", cm)
	}
	ds, err := c.SessionDiscard(ctx, sid)
	must(t, err, "session discard")
	if ds["discarded"] != float64(0) {
		t.Errorf("discard of an unknown session must discard nothing: %v", ds)
	}
}

// Identity governance: labels, recorded uncertainty, CSV lookup tables, erase.
func TestRealIdentityGovernance(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	id := relata.NewIdentityClient(c)
	who := strings.ToLower(uniq("ig")) + "@example.com"
	// KNOWN CONTRACT DRIFT (all three SDKs): Label posts {identity,label,confidence}
	// but POST /v1/identity/label requires {left,right,is_match}. The call is a
	// typed 422, never a silent success.
	_, err := id.Label(ctx, who, "vip", &relata.LabelOptions{Confidence: 0.9, SourceID: "analyst-1"})
	wantProblem(t, err, 422, "")
	if r, err := id.RecordUncertainty(ctx, who, "two people share this mailbox", &relata.RecordUncertaintyOptions{SuggestedAlternatives: []string{"a@example.com", "b@example.com"}}); err != nil {
		t.Errorf("uncertainty: %v", err)
	} else {
		t.Logf("uncertainty: %v", r)
	}
	// KNOWN CONTRACT DRIFT (all three SDKs): RegisterLookup posts JSON with
	// inline csv_data but the route takes a raw `REGISTER LOOKUP ... FROM
	// '<server path>'` statement. Typed 400, never a silent success.
	name := uniq("tbl")
	csv := "code,country,risk\nPK,Pakistan,medium\n"
	_, err = id.RegisterLookup(ctx, name, csv, "code", &relata.RegisterLookupOptions{ValueColumns: []string{"country", "risk"}})
	wantProblem(t, err, 400, "")
	ls, err := id.ListLookups(ctx)
	must(t, err, "list lookups")
	if strings.Contains(str(ls), name) {
		t.Errorf("a refused registration must not be listed: %v", ls)
	}
	got, err := id.InvokeLookup(ctx, name, "PK")
	if err == nil && strings.Contains(str(got), "Pakistan") {
		t.Errorf("an unregistered lookup must not return data: %v", got)
	}
	// Erasure without CERTIFY is refused with a typed 400, not executed.
	_, err = id.EraseSubject(ctx, who, "gdpr", "investigation", &relata.EraseSubjectOptions{Certify: false, CertifySet: true})
	wantProblem(t, err, 400, "")
}

// Media work queue: list, claim lifecycle on an empty queue and typed errors.
func TestRealMediaWork(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	m := relata.NewMediaWorkClient(c)
	if _, err := m.List(ctx, "bogus", 5); err == nil {
		t.Errorf("an invalid state filter must be refused client-side")
	}
	l, err := m.List(ctx, relata.MediaWorkQueued, 5)
	if err != nil {
		t.Logf("media work list: %v", err)
		wantProblemAny(t, err)
		return
	}
	t.Logf("media work: %v", l)
	_, err = m.Get(ctx, "no-such-work-"+uniq("w"))
	wantProblem(t, err, 404, "")
	cl, err := m.Claim(ctx, "worker-"+uniq("w"), &relata.MediaWorkClaimOptions{MaxItems: 1, LeaseMs: 1000})
	if err != nil {
		wantProblemAny(t, err)
	} else {
		t.Logf("claim: %v", cl)
	}
	for name, call := range map[string]func() (map[string]any, error){
		"heartbeat": func() (map[string]any, error) { return m.Heartbeat(ctx, "no-such-work", 1, 1000) },
		"complete":  func() (map[string]any, error) { return m.Complete(ctx, "no-such-work", 1, "v1", 1, 0) },
		"fail":      func() (map[string]any, error) { return m.Fail(ctx, "no-such-work", 1, "retryable", "E1", "detail") },
		"requeue":   func() (map[string]any, error) { return m.Requeue(ctx, "no-such-work") },
	} {
		if _, err := call(); err == nil {
			t.Errorf("%s on an unknown work item must fail", name)
		} else {
			wantProblemAny(t, err)
		}
	}
	h := relata.MediaWorkClaimHeaders(42)
	if h[relata.MediaWorkClaimHeader] != "42" {
		t.Errorf("claim header: %v", h)
	}
}

// Feed broker: the broker is a separate service; with none listening the
// client must fail with a transport error, not hang or panic.
func TestRealFeedBrokerUnreachable(t *testing.T) {
	ctx := ctxT(t)
	fb := relata.NewFeedBrokerClient("http://127.0.0.1:1", &relata.ClientOptions{BearerToken: "x", Timeout: 2 * time.Second})
	if _, err := fb.Health(ctx); err == nil {
		t.Errorf("health against a dead broker must fail")
	}
	if _, err := fb.Channels(ctx); err == nil {
		t.Errorf("channels against a dead broker must fail")
	}
	if _, err := fb.Publish(ctx, "did:example:1", map[string]any{"title": "x"}); err == nil {
		t.Errorf("publish against a dead broker must fail")
	}
	if err := fb.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
