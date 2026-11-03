package tests

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

func realMemory(t *testing.T, purpose string) *relata.Memory {
	t.Helper()
	tok := os.Getenv("RELATA_TOKEN")
	if tok == "" {
		t.Skip("RELATA_TOKEN not set")
	}
	m, err := relata.NewMemory(realHost(), purpose, &relata.MemoryOptions{BearerToken: tok})
	must(t, err, "NewMemory")
	return m
}

// Agent-memory journey: add, recall with ranking/evidence, justify, review,
// associate, episodes, summarise, procedures, update and forget.
func TestRealJourneyMemory(t *testing.T) {
	ctx := ctxT(t)
	if _, err := relata.NewMemory(realHost(), " ", nil); err == nil {
		t.Fatal("empty purpose must be refused client-side")
	}
	m := realMemory(t, "agent-notes")
	tag := uniq("zebra")
	id1, err := m.Add(ctx, "Alice prefers dark mode in the "+tag+" console", relata.WithConfidence(0.9), relata.WithMemoryClass("semantic"))
	must(t, err, "add")
	if id1 == "" {
		t.Fatal("add returned no id")
	}
	id2, err := m.Add(ctx, "Bob reviewed the "+tag+" incident runbook", relata.WithMemoryClass("episodic"), relata.WithSessionID("sess-"+tag))
	must(t, err, "add 2")
	batch, err := m.AddBatch(ctx, []any{"batch note one " + tag, map[string]any{"content": "batch note two " + tag}})
	must(t, err, "add batch")
	if len(batch) != 2 || batch[0] == "" {
		t.Fatalf("batch ids: %v", batch)
	}

	var rows []map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		rows, err = m.Search(ctx, "dark mode "+tag, relata.WithTopK(5))
		must(t, err, "search")
		if len(rows) > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if len(rows) == 0 {
		t.Fatal("recall returned nothing for a memory just added")
	}
	hit := false
	for _, r := range rows {
		t.Logf("recall: score=%v conf=%v %v", r["score"], r["confidence"], r["content"])
		if strings.Contains(str(r["content"]), "dark mode") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("the dark-mode memory is missing from recall: %v", rows)
	}
	det, err := m.SearchDetailed(ctx, "dark mode "+tag, relata.WithTopK(3), relata.WithMinConfidence(0.1), relata.WithRecencyHalfLife(3600), relata.WithBudgetTokens(4000), relata.WithStabilityDays(30), relata.WithCancelThreshold(0.99), relata.WithAsOf(time.Now().Add(time.Minute).UTC().Format(time.RFC3339)))
	must(t, err, "search detailed")
	t.Logf("detailed: rows=%d cost=%d cancelled=%v", len(det.Rows), det.RecallCostTokens, det.Cancelled)

	if body, err := m.SearchStream(ctx, "dark mode "+tag, relata.WithTopK(3)); err != nil {
		t.Errorf("recall stream: %v", err)
	} else {
		b, _ := io.ReadAll(body)
		body.Close()
		if !strings.Contains(string(b), "data:") {
			t.Errorf("recall stream had no SSE frames: %q", b)
		}
	}

	g, err := m.Get(ctx, id1)
	must(t, err, "get")
	if !strings.Contains(str(g), "dark mode") {
		t.Fatalf("get: %v", g)
	}
	if _, err := m.Justify(ctx, id1); err != nil {
		t.Errorf("justify: %v", err)
	}
	if _, err := m.Resolve(ctx, id1); err != nil {
		t.Errorf("resolve: %v", err)
	}
	if _, err := m.Review(ctx, id1, "approve", "verified by journey"); err != nil {
		t.Errorf("review: %v", err)
	}
	if _, err := m.Associate(ctx, id1, id2, "related_to"); err != nil {
		t.Errorf("associate: %v", err)
	}
	if _, err := m.AssociateBatch(ctx, []map[string]any{{"from_id": id1, "to_id": batch[0], "relation": "supports"}}); err != nil {
		t.Errorf("associate batch: %v", err)
	}
	eps, err := m.Episodes(ctx, relata.WithSessionID("sess-"+tag))
	if err != nil {
		t.Errorf("episodes: %v", err)
	}
	t.Logf("episodes=%d", len(eps))
	if _, err := m.Summarise(ctx, []string{id1, id2}, relata.WithSummaryContent("Alice and Bob notes "+tag)); err != nil {
		t.Errorf("summarise: %v", err)
	}
	if _, err := m.RememberProcedure(ctx, "agent-"+tag, "triage", "1. check alerts 2. page oncall"); err != nil {
		t.Errorf("remember procedure: %v", err)
	}
	pr, err := m.RecallProcedure(ctx, "agent-"+tag, &relata.RecallProcedureOptions{Name: "triage"})
	if err != nil || !strings.Contains(str(pr), "page oncall") {
		t.Errorf("recall procedure: %v %v", pr, err)
	}
	newID, err := m.Update(ctx, id1, "Alice prefers LIGHT mode in the "+tag+" console")
	must(t, err, "update")
	t.Logf("updated id %q -> %q", id1, newID)
	if _, err := m.Forget(ctx, id2); err != nil {
		t.Errorf("forget: %v", err)
	}
	rows2, _ := m.Search(ctx, "Bob runbook "+tag, relata.WithTopK(5))
	for _, r := range rows2 {
		if str(r["id"]) == id2 {
			t.Errorf("forgotten memory still recalled: %v", r)
		}
	}
	// A purposeless recall is a typed governance refusal at the HTTP layer.
	tok := os.Getenv("RELATA_TOKEN")
	bad, _ := relata.NewMemory(realHost(), "x", &relata.MemoryOptions{BearerToken: tok + "bad"})
	_, err = bad.Search(ctx, "anything")
	wantProblemAny(t, err)
}

// Backup journey: create a snapshot, list it, compact, restore and poll.
func TestRealJourneyBackupRestore(t *testing.T) {
	ctx := ctxT(t)
	a := adminClient(t)
	ty := uniq("RbItem")
	c := realClient(t)
	ingestRowsWait(t, c, ty, []map[string]any{{"_pk": "b1", "v": 1}})
	b := relata.NewBackupClient(a)
	ref, err := b.Create(ctx, &relata.CreateBackupOptions{Kind: "full"})
	must(t, err, "create backup")
	if ref.SnapshotID == "" || ref.SizeBytes <= 0 || ref.SHA256 == "" {
		t.Fatalf("backup ref incomplete: %+v", ref)
	}
	list, err := b.List(ctx)
	must(t, err, "list backups")
	found := false
	for _, r := range list {
		if r.SnapshotID == ref.SnapshotID {
			found = true
		}
	}
	if !found {
		t.Fatalf("snapshot %s not listed (%d snapshots)", ref.SnapshotID, len(list))
	}
	if _, err := b.Compact(ctx); err != nil {
		t.Errorf("compact: %v", err)
	}
	rid, err := b.Restore(ctx, ref.SnapshotID, nil)
	if err != nil {
		t.Logf("restore refused: %v", err)
		wantProblemAny(t, err)
		return
	}
	st, err := b.RestoreStatus(ctx, rid)
	must(t, err, "restore status")
	if st.RestoreID != rid {
		t.Fatalf("status id: %+v", st)
	}
	fin, err := b.WaitForRestore(ctx, rid, &relata.WaitForRestoreOptions{TimeoutSecs: 60, PollIntervalSecs: 1})
	must(t, err, "wait restore")
	if !fin.IsCompleted() && !fin.IsFailed() {
		t.Fatalf("restore not terminal: %+v", fin)
	}
	t.Logf("restore terminal: %+v", fin)
}
