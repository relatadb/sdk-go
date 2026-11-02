package tests

import (
	"io"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// Streaming journey: NDJSON row stream with completion proof, raw Arrow bytes,
// a live SQL watch that pushes the row inserted after it opened, graph
// changes and the A2A task stream's failure mode.
func TestRealJourneyStreaming(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	st := relata.NewStreamingClient(c)
	ty := uniq("RsRow")
	var rows []map[string]any
	for i := 0; i < 120; i++ {
		rows = append(rows, map[string]any{"_pk": uniq("r"), "n": i})
	}
	ingestRowsWait(t, c, ty, rows)

	it, err := st.QueryRows(ctx, "SELECT n FROM "+ty+" ORDER BY n", &relata.StreamingQueryOptions{Purpose: "analytics"})
	must(t, err, "query rows")
	n := 0
	for it.Next() {
		if it.Row() == nil {
			t.Fatal("nil row")
		}
		n++
	}
	if it.Err() != nil || n != 120 {
		t.Fatalf("streamed %d rows (want 120), err=%v", n, it.Err())
	}
	if err := it.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	// A bad statement fails the stream with a typed error, not silent emptiness.
	if bad, err := st.QueryRows(ctx, "SELECT FROM WHERE", &relata.StreamingQueryOptions{Purpose: "analytics"}); err == nil {
		for bad.Next() {
		}
		if bad.Err() == nil {
			t.Errorf("a malformed statement must surface an error")
		}
	} else {
		wantProblemAny(t, err)
	}

	bi, err := st.QueryArrowRaw(ctx, "SELECT n FROM "+ty+" ORDER BY n", &relata.StreamingQueryOptions{Purpose: "analytics"})
	if err != nil {
		t.Logf("arrow stream: %v", err)
		wantProblemAny(t, err)
	} else {
		all, err := io.ReadAll(bi)
		if err != nil {
			t.Errorf("arrow stream read: %v", err)
		}
		total := len(all)
		bi.Close()
		if total == 0 {
			t.Errorf("arrow stream returned no bytes")
		}
	}

	// Live watch: a row inserted after the stream opens must be pushed.
	wty := uniq("RsWatch")
	ingestRowsWait(t, c, wty, []map[string]any{{"_pk": "w0", "v": 0}})
	no := false
	w, err := st.Watch(ctx, "SELECT _pk, v FROM "+wty, &relata.WatchOptions{Purpose: "analytics"})
	if err != nil {
		t.Logf("watch: %v", err)
		wantProblemAny(t, err)
	} else {
		defer w.Close()
		got := make(chan string, 16)
		go func() {
			for w.Next() {
				got <- str(w.Event().Data) + w.Event().Type
			}
			close(got)
		}()
		time.Sleep(500 * time.Millisecond)
		if _, err := c.IngestDocument(ctx, []map[string]any{{"_type": wty, "_pk": "w1", "v": 1}}, "analytics"); err != nil {
			t.Fatalf("ingest during watch: %v", err)
		}
		deadline := time.After(15 * time.Second)
		seen := false
		for !seen {
			select {
			case ev, ok := <-got:
				if !ok {
					t.Errorf("watch stream ended before pushing the new row")
					seen = true
				} else if strings.Contains(ev, "result_changed") {
					t.Logf("watch pushed: %.200s", ev)
					seen = true
				}
			case <-deadline:
				t.Errorf("watch did not push the inserted row within 15s")
				seen = true
			}
		}
	}

	gc, err := st.GraphChanges(ctx, &relata.GraphChangesOptions{Type: ty, Reconnect: &no})
	if err != nil {
		t.Logf("graph changes: %v", err)
		wantProblemAny(t, err)
	} else {
		gc.Close()
	}
	a, err := st.A2ATaskStream(ctx, "no-such-task-"+uniq("t"), &no)
	if err == nil {
		for a.Next() {
		}
		a.Close()
	} else {
		wantProblemAny(t, err)
	}
}
