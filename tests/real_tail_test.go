package tests

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// App-owned summary: the SDK recalls governed rows, the application completes,
// the SDK stores the summary back through Remember.
func TestRealSummariseMemory(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	m := relata.NewMcpClient(c)
	const P = "agent-notes"
	tag := uniq("quokka")
	if _, err := m.Remember(ctx, "The "+tag+" gateway was patched on Monday", P, &relata.RememberOptions{Confidence: 0.9}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	var sawPrompt string
	opts := relata.SummariseMemoryOptions{Purpose: P, TopK: 5, Complete: func(_ context.Context, prompt string, _ int) (relata.Completion, error) {
		sawPrompt = prompt
		return relata.Completion{Text: "Summary: the " + tag + " gateway was patched.", ModelID: "test-model"}, nil
	}}
	var out map[string]any
	var err error
	for i := 0; i < 40; i++ {
		out, err = m.SummariseMemory(ctx, tag+" gateway", opts)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "no source rows") {
			t.Fatalf("summarise: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	must(t, err, "summarise memory")
	if !strings.Contains(sawPrompt, tag) {
		t.Errorf("the completion prompt must carry the recalled memory: %q", sawPrompt)
	}
	if len(out) == 0 {
		t.Errorf("summary write returned nothing")
	}
	if _, err := m.SummariseMemory(ctx, "x", relata.SummariseMemoryOptions{Purpose: P}); err == nil {
		t.Errorf("a missing completion callback must be refused")
	}
}

func TestRealRestoreStatusUnknown(t *testing.T) {
	a := adminClient(t)
	ctx := ctxT(t)
	b := relata.NewBackupClient(a)
	_, err := b.RestoreStatus(ctx, "no-such-restore-"+uniq("r"))
	wantProblemAny(t, err)
	_, err = b.WaitForRestore(ctx, "no-such-restore-"+uniq("r"), &relata.WaitForRestoreOptions{TimeoutSecs: 2, PollIntervalSecs: 1})
	wantProblemAny(t, err)
	if !(relata.RestoreStatus{Status: "completed"}).IsCompleted() || !(relata.RestoreStatus{Status: "failed"}).IsFailed() {
		t.Errorf("status predicates")
	}
}

func TestRealStreamIteratorsAndCanonical(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	st := relata.NewStreamingClient(c)
	ty := uniq("TlRow")
	ingestRowsWait(t, c, ty, []map[string]any{{"_pk": "a", "n": 1}, {"_pk": "b", "n": 2}})
	it, err := st.QueryArrowRaw(ctx, "SELECT n FROM "+ty+" ORDER BY n", &relata.StreamingQueryOptions{Purpose: "analytics"})
	if err != nil {
		wantProblemAny(t, err)
	} else {
		// Next reports whether bytes remain; the iterator is also the io.Reader.
		if !it.Next() {
			t.Errorf("arrow stream is empty")
		}
		b, rerr := io.ReadAll(it)
		if rerr != nil || len(b) == 0 {
			t.Errorf("arrow bytes=%d err=%v", len(b), rerr)
		}
		if it.Next() || it.Err() != nil {
			t.Errorf("a drained stream must end cleanly: %v", it.Err())
		}
		_ = it.Close()
	}
	// Canonical validators: accept real values, reject near misses.
	for name, ok := range map[string]bool{
		"phone":  relata.ValidatePhone("+14155550101") && !relata.ValidatePhone("not a phone"),
		"imei":   relata.ValidateImei("490154203237518") && !relata.ValidateImei("490154203237519"),
		"vin":    relata.ValidateVin("1HGCM82633A004352") && !relata.ValidateVin("1HGCM82633A00435"),
		"msisdn": relata.ValidateMsisdn("+923001234567") && !relata.ValidateMsisdn("abc"),
		"ipv4":   relata.ValidateIpv4("192.168.1.1") && !relata.ValidateIpv4("999.1.1.1"),
	} {
		if !ok {
			t.Errorf("canonical %s validator wrong", name)
		}
	}
	_, err = relata.NormalizeEmail("nope")
	if ce := relata.AsCanonicalError(err); ce == nil || ce.Error() == "" {
		t.Errorf("a bad email must be a typed CanonicalError: %v", err)
	}
}
