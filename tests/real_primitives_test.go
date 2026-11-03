package tests

import (
	"strings"
	"testing"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	}
	return -1
}

// Application primitives: gap-free sequences and a leased, fenced job queue.
func TestRealAppPrimitives(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	p := relata.NewAppPrimitivesClient(c)

	seq := uniq("seq")
	a, err := p.SequenceNext(ctx, seq, 0, "")
	must(t, err, "seq next")
	b, err := p.SequenceNext(ctx, seq, 5, "")
	must(t, err, "seq next 5")
	t.Logf("seq: %v %v", a, b)
	g, err := p.SequenceGet(ctx, seq)
	must(t, err, "seq get")
	if !strings.Contains(str(g), seq) && len(g) == 0 {
		t.Fatalf("seq get: %v", g)
	}
	key := uniq("idem")
	k1, err := p.SequenceNext(ctx, seq, 2, key)
	must(t, err, "seq idem 1")
	k2, err := p.SequenceNext(ctx, seq, 2, key)
	must(t, err, "seq idem 2")
	if k1["start"] != k2["start"] || k1["end"] != k2["end"] || k2["duplicate"] != true {
		t.Errorf("idempotent sequence retry must replay the same allocation: %v vs %v", k1, k2)
	}

	q := uniq("queue")
	j1, err := p.Enqueue(ctx, q, map[string]any{"task": "scan", "n": 1}, nil)
	must(t, err, "enqueue 1")
	j2, err := p.Enqueue(ctx, q, map[string]any{"task": "scan", "n": 2}, nil)
	must(t, err, "enqueue 2")
	id1 := str(j1["id"])
	if id1 == "" || id1 == "<nil>" {
		id1 = str(j1["jobId"])
	}
	t.Logf("enqueued: %v %v", j1, j2)
	jobs, err := p.Claim(ctx, q, "worker-a", 30000, 1)
	must(t, err, "claim")
	if len(jobs) != 1 {
		t.Fatalf("claim of max 1 returned %d jobs", len(jobs))
	}
	job := jobs[0].(map[string]any)
	jid := str(job["id"])
	if jid == "" || jid == "<nil>" {
		jid = str(job["jobId"])
	}
	fence := num(job["fencingToken"])
	if fence < 0 {
		t.Fatalf("claimed job has no fencing token: %v", job)
	}
	if _, err := p.Renew(ctx, jid, fence, 60000); err != nil {
		t.Errorf("renew: %v", err)
	}
	// A stale fencing token must be refused.
	_, err = p.Complete(ctx, jid, fence+1000, nil)
	wantProblemAny(t, err)
	if _, err := p.Complete(ctx, jid, fence, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	gj, err := p.GetJob(ctx, q, jid)
	must(t, err, "get job")
	if gj["state"] != "done" {
		t.Errorf("completed job state: %v", gj)
	}
	jobs2, err := p.Claim(ctx, q, "worker-b", 30000, 5)
	must(t, err, "claim 2")
	if len(jobs2) != 1 {
		t.Fatalf("second claim must get the remaining job, got %d", len(jobs2))
	}
	j := jobs2[0].(map[string]any)
	jid2 := str(j["id"])
	if jid2 == "" || jid2 == "<nil>" {
		jid2 = str(j["jobId"])
	}
	if _, err := p.Fail(ctx, jid2, num(j["fencingToken"]), map[string]any{"error": "boom", "retryable": false}); err != nil {
		t.Errorf("fail: %v", err)
	}
	lst, err := p.ListJobs(ctx, q, "", 10)
	must(t, err, "list jobs")
	if !strings.Contains(str(lst), jid) {
		t.Errorf("list jobs: %v", lst)
	}
	_ = id1
}

// A2A: agent card, task submission and checkpoint round trip.
func TestRealA2A(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	a := relata.NewA2AClient(c)
	card, err := a.AgentCard(ctx)
	must(t, err, "agent card")
	if len(card) == 0 {
		t.Fatalf("empty agent card")
	}
	thread := uniq("thread")
	if _, err := a.SaveCheckpoint(ctx, thread, "cp1", map[string]any{"state": map[string]any{"step": 1}}); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	if _, err := a.SaveCheckpoint(ctx, thread, "cp2", map[string]any{"state": map[string]any{"step": 2}}); err != nil {
		t.Fatalf("save checkpoint 2: %v", err)
	}
	cps, err := a.ListCheckpoints(ctx, thread)
	must(t, err, "list checkpoints")
	if len(cps) != 2 || !strings.Contains(str(cps), "cp1") || !strings.Contains(str(cps), "cp2") {
		t.Fatalf("checkpoints: %v", cps)
	}
	ld, err := a.LoadCheckpoint(ctx, thread, "cp2")
	must(t, err, "load checkpoint")
	if !strings.Contains(str(ld), "2") {
		t.Fatalf("checkpoint payload lost: %v", ld)
	}
	_, err = a.LoadCheckpoint(ctx, thread, "missing")
	wantProblem(t, err, 404, "")
	task, err := a.SubmitTask(ctx, map[string]any{"skill": "echo", "input": map[string]any{"text": "hi"}})
	if err != nil {
		t.Logf("submit task: %v", err)
		wantProblemAny(t, err)
	} else if id := str(task["id"]); id != "" && id != "<nil>" {
		if _, err := a.GetTask(ctx, id); err != nil {
			t.Errorf("get task: %v", err)
		}
	}
	_, err = a.GetTask(ctx, "no-such-task-"+uniq("t"))
	wantProblemAny(t, err)
}
