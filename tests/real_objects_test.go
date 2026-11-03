package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

func i64(v int64) *int64 { return &v }

// verOf reads the object "version" the conditional doors return. Versions are a
// monotonic store-wide sequence, not a per-object counter.
func verOf(t *testing.T, m map[string]any) int64 {
	t.Helper()
	f, ok := m["version"].(float64)
	if !ok || f <= 0 {
		t.Fatalf("no version in %v", m)
	}
	return int64(f)
}

// TestRealObjectsConditionalWrites covers the atomic object door: create-only,
// compare-and-set by _version, stale 412 (with current_version), delete, and the
// atomic multi-object commit bundle with idempotent replay.
func TestRealObjectsConditionalWrites(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	obj := relata.NewObjectClient(c)
	ty := uniq("RoCase")

	created, err := obj.Upsert(ctx, ty, "c1", map[string]any{"title": "first", "sev": 3}, &relata.UpsertOptions{IfAbsent: true})
	must(t, err, "create-only")
	v1 := verOf(t, created)
	if created["id"] != ty && created["type"] != ty {
		t.Fatalf("create-only: %v", created)
	}
	_, err = obj.Upsert(ctx, ty, "c1", map[string]any{"title": "dup"}, &relata.UpsertOptions{IfAbsent: true})
	wantProblem(t, err, 412, "")
	got, err := obj.Get(ctx, ty, "c1")
	must(t, err, "get")
	if got == nil || got["title"] != "first" || str(got["_version"]) != str(v1) {
		t.Fatalf("get: %v", got)
	}
	up, err := obj.Upsert(ctx, ty, "c1", map[string]any{"title": "second", "sev": 5}, &relata.UpsertOptions{ExpectedVersion: i64(v1)})
	must(t, err, "cas update")
	v2 := verOf(t, up)
	if v2 <= v1 {
		t.Fatalf("version must advance: %d -> %d", v1, v2)
	}
	_, err = obj.Upsert(ctx, ty, "c1", map[string]any{"title": "stale"}, &relata.UpsertOptions{ExpectedVersion: i64(v1)})
	re := wantProblem(t, err, 412, "")
	if !errors.Is(err, relata.ErrPreconditionFailed) || re.CurrentVersion == nil || *re.CurrentVersion != v2 {
		t.Fatalf("stale write must carry current_version=%d and wrap ErrPreconditionFailed: %+v", v2, re)
	}
	if _, err := obj.Upsert(ctx, ty, "c1", nil, &relata.UpsertOptions{IfAbsent: true, ExpectedVersion: i64(v2)}); err == nil {
		t.Fatal("IfAbsent+ExpectedVersion must be rejected client-side")
	}

	// Missing object: Get returns (nil, nil).
	if g, err := obj.Get(ctx, ty, "nope"); err != nil || g != nil {
		t.Fatalf("missing get: %v %v", g, err)
	}

	// Plain (non-conditional) upsert + batch upsert flow through ingest.
	ack, err := obj.Upsert(ctx, ty, "c2", map[string]any{"title": "plain"}, &relata.UpsertOptions{Source: "sdk-real"})
	must(t, err, "plain upsert")
	if str(ack["rows_queued"]) != "1" {
		t.Fatalf("plain upsert ack: %v", ack)
	}
	bu, err := obj.BatchUpsert(ctx, ty, []map[string]any{{"id": "c3", "title": "b3"}, {"id": "c4", "title": "b4"}}, &relata.UpsertOptions{Source: "sdk-real"})
	must(t, err, "batch upsert")
	if str(bu["rows_queued"]) != "2" {
		t.Fatalf("batch upsert ack: %v", bu)
	}
	waitRows(t, c, "SELECT * FROM "+ty, 4)

	// Atomic commit bundle: create two, then replay by idempotency key.
	k := uniq("commit")
	ops := []relata.CommitOperation{
		{Op: "create", Type: ty, ID: "k1", Data: map[string]any{"title": "k1"}},
		{Op: "create", Type: ty, ID: "k2", Data: map[string]any{"title": "k2"}},
		{Op: "update", Type: ty, ID: "c1", ExpectedVersion: i64(v2), Data: map[string]any{"title": "third"}},
	}
	res, err := obj.Commit(ctx, ops, &relata.CommitOptions{IdempotencyKey: k})
	must(t, err, "commit")
	if len(res.Operations) != 3 || res.CommitID == "" || res.Replayed || res.Operations[2].Version <= v2 {
		t.Fatalf("commit result: %+v", res)
	}
	rep, err := c.Commit(ctx, ops, &relata.CommitOptions{IdempotencyKey: k})
	must(t, err, "commit replay")
	if !rep.Replayed || rep.CommitID != res.CommitID {
		t.Fatalf("replay must return the original: %+v vs %+v", rep, res)
	}
	// A bundle with one stale precondition applies nothing (atomic) and names the failing op.
	_, err = obj.Commit(ctx, []relata.CommitOperation{
		{Op: "create", Type: ty, ID: "k3", Data: map[string]any{"title": "k3"}},
		{Op: "update", Type: ty, ID: "c1", ExpectedVersion: i64(v1), Data: map[string]any{"title": "x"}},
	}, nil)
	fe := wantProblem(t, err, 412, "")
	if fe.FailedOperation == nil || *fe.FailedOperation != 1 {
		t.Fatalf("failed_operation must be 1: %+v", fe)
	}
	if g, _ := obj.Get(ctx, ty, "k3"); g != nil {
		t.Fatalf("atomic bundle leaked k3: %v", g)
	}

	// Conditional delete.
	_, err = obj.Delete(ctx, ty, "c1", &relata.DeleteOptions{ExpectedVersion: i64(v1)})
	wantProblem(t, err, 412, "")
	if _, err := obj.Delete(ctx, ty, "c1", &relata.DeleteOptions{ExpectedVersion: i64(res.Operations[2].Version)}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if g, _ := obj.Get(ctx, ty, "c1"); g != nil {
		t.Fatalf("deleted object still readable: %v", g)
	}
}

// TestRealChangeFeed reads the durable change feed: ordered at-least-once events
// with stable ids, cursor resume, per-type filter, ChangePages and a follower that
// picks up a write made after it started.
func TestRealChangeFeed(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	obj := relata.NewObjectClient(c)
	ty := uniq("RfFeed")

	head, err := obj.Changes(ctx, relata.ChangesOptions{Start: "latest", Type: ty})
	must(t, err, "head")
	cursor := head.NextCursor
	for i := 1; i <= 3; i++ {
		_, err := obj.Upsert(ctx, ty, fmt.Sprintf("f%d", i), map[string]any{"n": i}, &relata.UpsertOptions{IfAbsent: true})
		must(t, err, "write")
	}
	var seen []relata.ChangeEvent
	deadline := time.Now().Add(20 * time.Second)
	for len(seen) < 3 && time.Now().Before(deadline) {
		p, err := obj.Changes(ctx, relata.ChangesOptions{Cursor: cursor, Type: ty, Limit: 2})
		must(t, err, "changes")
		seen = append(seen, p.Events...)
		cursor = p.NextCursor
		if len(p.Events) == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("want 3 events got %d: %+v", len(seen), seen)
	}
	for i, e := range seen {
		if e.Type != ty || e.ID != fmt.Sprintf("f%d", i+1) || e.EventID == "" || e.Kind == "" || e.Version <= 0 {
			t.Fatalf("event %d: %+v", i, e)
		}
		if i > 0 && e.Seq <= seen[i-1].Seq {
			t.Fatalf("events out of order: %+v", seen)
		}
	}

	// ChangePages from the start: handler sees every event for the type.
	var viaPages int
	last, err := obj.ChangePages(ctx, relata.ChangesOptions{Type: ty, Limit: 1}, func(p *relata.ChangesPage) error {
		viaPages += len(p.Events)
		return nil
	})
	must(t, err, "change pages")
	if viaPages != 3 || last == "" {
		t.Fatalf("ChangePages saw %d events, last cursor %q", viaPages, last)
	}

	// Follower: starts at "latest", must see a write made after it started, then stops on cancel.
	fctx, cancel := context.WithCancel(ctx)
	got := make(chan string, 8)
	done := make(chan error, 1)
	go func() {
		_, err := obj.FollowChanges(fctx, relata.ChangesOptions{Type: ty, Start: "latest", Limit: 10},
			relata.FollowOptions{PollInterval: 100 * time.Millisecond}, func(p *relata.ChangesPage) error {
				for _, e := range p.Events {
					got <- e.ID
				}
				return nil
			})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	_, err = obj.Upsert(ctx, ty, "late", map[string]any{"n": 99}, &relata.UpsertOptions{IfAbsent: true})
	must(t, err, "late write")
	select {
	case id := <-got:
		if id != "late" {
			t.Fatalf("follower delivered %q", id)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("follower never saw the post-start write")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follower must stop with context.Canceled, got %v", err)
	}

	// A malformed cursor is a typed problem, not a hang.
	_, err = obj.Changes(ctx, relata.ChangesOptions{Cursor: "not-a-cursor"})
	wantProblemAny(t, err)
}

// TestRealNamespaceExportAndTyped covers Namespace (schemaless write/query/get/
// delete), UpsertTyped, ExportData (json + csv), and the typed search door.
func TestRealNamespaceExportAndTyped(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)

	ns := c.Namespace(uniq("RnNs"))
	w, err := ns.Write(ctx, []map[string]any{{"id": "d1", "body": "red apple pie"}, {"id": "d2", "body": "green apple tart"}, {"id": "d3", "body": "banana split"}}, nil)
	must(t, err, "ns write")
	if str(w["rows_queued"]) != "3" && str(w["inserted"]) != "3" {
		t.Logf("ns write ack: %v", w)
	}
	waitRows(t, c, "SELECT * FROM "+ns.Name, 3)
	g, err := ns.Get(ctx, "d2", nil)
	must(t, err, "ns get")
	if g == nil || g["body"] != "green apple tart" {
		t.Fatalf("ns get: %v", g)
	}
	var q *relata.QueryResult
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		q, err = ns.Query(ctx, relata.SearchRequest{Text: "apple", MatchColumn: "body", Limit: 10})
		must(t, err, "ns query")
		if len(q.Rows) == 2 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if len(q.Rows) != 2 {
		t.Fatalf("bm25 'apple' must match 2 docs, got %d: %v", len(q.Rows), q.Rows)
	}
	if _, err := c.Namespace("bad name!").Get(ctx, "x", nil); err == nil {
		t.Fatal("invalid namespace name must be rejected client-side")
	}

	// Typed upsert.
	type Asset struct {
		Host string `json:"host"`
		Crit int    `json:"crit"`
	}
	ty := uniq("RnAsset")
	if _, err := c.UpsertTyped(ctx, ty, "srv-1", Asset{Host: "srv-1.corp", Crit: 9}); err != nil {
		t.Fatalf("upsert typed: %v", err)
	}
	rows := waitRows(t, c, "SELECT host, crit FROM "+ty+" WHERE _pk = 'srv-1'", 1)
	if rows[0]["host"] != "srv-1.corp" || str(rows[0]["crit"]) != "9" {
		t.Fatalf("typed row: %v", rows[0])
	}

	// Export.
	ex, err := c.ExportData(ctx, ty, "json")
	must(t, err, "export json")
	if str(ex["rows"]) != "1" || ex["type"] != ty {
		t.Fatalf("export json: %v", ex)
	}
	csv, err := c.ExportData(ctx, ty, "csv")
	must(t, err, "export csv")
	body, _ := csv["content"].(string)
	if !strings.Contains(body, "srv-1.corp") || !strings.Contains(body, "host") {
		t.Fatalf("export csv: %v", csv)
	}
	_, err = c.ExportData(ctx, "NoSuchType"+ty, "json")
	wantProblemAny(t, err)

	// Search door + options.
	for i := 0; i < 1; i++ {
		var resp *relata.SearchResponse
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			resp, err = c.Search(ctx, "apple", ns.Name, relata.WithSearchLimit(5), relata.WithHighlight(), relata.WithMatchingStrategy("all"))
			must(t, err, "search")
			if resp.Total == 2 {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
		if resp.Total != 2 || len(resp.Hits) != 2 || resp.Hits[0].Score <= 0 || resp.Hits[0].ObjectType != ns.Name {
			t.Fatalf("search: %+v", resp)
		}
	}
	ms, err := c.MultiSearch(ctx, []map[string]any{{"query": "apple", "type": ns.Name, "limit": 5}, {"query": "banana", "type": ns.Name}})
	must(t, err, "multisearch")
	if rs, _ := ms["results"].([]any); len(rs) != 2 {
		t.Fatalf("multisearch: %v", ms)
	}

	// Delete the namespace's rows.
	if _, err := ns.DeleteAll(ctx, nil); err != nil {
		t.Fatalf("ns delete: %v", err)
	}
}
