package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

// Ingestion journey: queued document ingest with task polling, CSV, auto
// schema, streaming iterator, CDR, STIX, contracts, quarantine, checkpoint.
func TestRealJourneyIngestion(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)

	ty := uniq("RiDoc")
	rcpt, err := c.IngestDocument(ctx, []map[string]any{{"_type": ty, "_pk": "d1", "title": "alpha"}, {"_type": ty, "_pk": "d2", "title": "beta"}}, "ingest")
	must(t, err, "ingest document")
	if rcpt.TaskID == "" || rcpt.RowsQueued != 2 {
		t.Fatalf("receipt: %+v", rcpt)
	}
	var st *relata.IngestDocumentTaskStatus
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		st, err = c.IngestDocumentStatus(ctx, rcpt.TaskID)
		must(t, err, "task status")
		if st.Status == "complete" {
			break
		}
	}
	t.Logf("task final: %+v", st)
	if st.TaskID != rcpt.TaskID || st.RowsTotal != 2 {
		t.Fatalf("task status: %+v", st)
	}
	_, err = c.IngestDocumentStatus(ctx, "no-such-task-"+uniq("x"))
	wantProblemAny(t, err)

	ing := relata.NewIngestClient(c)
	cty := uniq("RiCsv")
	csv := "_pk,name,score\nc1,ann,10\nc2,bo,20\n"
	if _, err := ing.BulkCSV(ctx, cty, csv, &relata.BulkOptions{Purpose: "ingest"}); err != nil {
		t.Fatalf("bulk csv: %v", err)
	}
	waitRows(t, c, "SELECT _pk, name FROM "+cty, 2)
	waitRows(t, c, "SELECT _pk, title FROM "+ty, 2)

	aty := uniq("RiAuto")
	if _, err := ing.IngestAuto(ctx, aty, []map[string]any{{"_pk": "a1", "n": 5, "label": "x"}, {"_pk": "a2", "n": 6, "label": "y"}}, &relata.AutoOptions{Purpose: "ingest"}); err != nil {
		t.Fatalf("ingest auto: %v", err)
	}
	waitRows(t, c, "SELECT _pk FROM "+aty, 2)

	ity := uniq("RiIter")
	ch := make(chan map[string]any)
	go func() {
		for i := 0; i < 25; i++ {
			ch <- map[string]any{"_pk": uniq("it"), "i": i}
		}
		close(ch)
	}()
	n, err := ing.IngestIter(ctx, ity, ch, "ingest", 10)
	must(t, err, "ingest iter")
	if n != 25 {
		t.Fatalf("iter ingested %d, want 25", n)
	}
	waitRows(t, c, "SELECT _pk FROM "+ity, 25)

	cdr, err := ing.IngestCDR(ctx, []map[string]any{{"caller": "+14155550100", "callee": "+14155550101", "start_time": "2026-01-01T00:00:00Z", "duration": 61}}, &relata.BulkOptions{Purpose: "ingest"})
	if err != nil {
		t.Errorf("ingest cdr: %v", err)
	} else {
		t.Logf("cdr receipt: %v", cdr)
	}
	bundle := map[string]any{"type": "bundle", "id": "bundle--00000000-0000-4000-8000-000000000001", "objects": []any{map[string]any{"type": "indicator", "spec_version": "2.1", "id": "indicator--00000000-0000-4000-8000-000000000002", "created": "2026-01-01T00:00:00.000Z", "modified": "2026-01-01T00:00:00.000Z", "pattern": "[ipv4-addr:value = '203.0.113.9']", "pattern_type": "stix", "valid_from": "2026-01-01T00:00:00Z"}}}
	// A bundle without relata extensions is not round-trippable: typed refusal
	// that names the skipped object (not a silent zero-row success).
	_, err = ing.ImportSTIX(ctx, bundle, &relata.BulkOptions{Purpose: "ingest"})
	wantProblem(t, err, 400, "")

}

// Data contracts, quarantine, idempotent retry and durable source checkpoints.
func TestRealJourneyIngestContracts(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	ing := relata.NewIngestClient(c)
	ctl := relata.NewIngestControlClient(c)
	ds := uniq("rcontract")
	ty := uniq("RiOrder")
	_, err := ctl.PutContract(ctx, ds, map[string]any{
		"version": 1, "on_invalid": "quarantine", "object_type": ty,
		"fields": []map[string]any{
			{"name": "qty", "type": "integer", "required": true, "min": 1},
		},
	})
	must(t, err, "put contract")
	got, err := ctl.GetContract(ctx, ds)
	must(t, err, "get contract")
	if !strings.Contains(str(got), "qty") {
		t.Fatalf("contract not stored: %v", got)
	}
	all, err := ctl.ListContracts(ctx)
	must(t, err, "list contracts")
	if !strings.Contains(str(all), ds) {
		t.Fatalf("contract %s missing from list: %v", ds, all)
	}
	rcpt, err := ing.Bulk(ctx, ty, []map[string]any{
		{"_pk": "o1", "qty": 3},
		{"_pk": "o2", "qty": "many"}, // wrong type: quarantined
		{"_pk": "o3"},                // missing required qty: quarantined
	}, &relata.BulkOptions{Purpose: "ingest", Durability: "sync", Contract: &relata.ContractRef{Dataset: ds}})
	must(t, err, "bulk with contract")
	t.Logf("contract receipt: %v", rcpt)
	waitRows(t, c, "SELECT _pk FROM "+ty, 1)
	q, err := ctl.ListQuarantine(ctx, &relata.QuarantineListOptions{Dataset: ds, Limit: 10})
	must(t, err, "list quarantine")
	items, _ := q["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 quarantined records, got %d: %v", len(items), q)
	}
	qual, err := ctl.ContractQuality(ctx, ds)
	must(t, err, "quality")
	t.Logf("quality: %v", qual)
	if id, ok := items[0].(map[string]any)["id"].(float64); ok {
		if err := ctl.DeleteQuarantine(ctx, uint64(id)); err != nil {
			t.Errorf("delete quarantine: %v", err)
		}
		q2, _ := ctl.ListQuarantine(ctx, &relata.QuarantineListOptions{Dataset: ds, Limit: 10})
		if it2, _ := q2["items"].([]any); len(it2) != 1 {
			t.Errorf("quarantine after delete: %v", q2)
		}
	} else {
		t.Errorf("quarantine entry has no numeric id: %v", items[0])
	}

	// Idempotent retry: same key + same rows replays; a different body conflicts.
	ik := uniq("idem")
	rows := []map[string]any{{"_pk": "i1", "v": 1}}
	ity := uniq("RiIdem")
	if _, err := ing.Bulk(ctx, ity, rows, &relata.BulkOptions{Purpose: "ingest", IdempotencyKey: ik}); err != nil {
		t.Fatalf("idem first: %v", err)
	}
	if _, err := ing.Bulk(ctx, ity, rows, &relata.BulkOptions{Purpose: "ingest", IdempotencyKey: ik}); err != nil {
		t.Fatalf("idem replay: %v", err)
	}
	_, err = ing.Bulk(ctx, ity, []map[string]any{{"_pk": "i2", "v": 2}}, &relata.BulkOptions{Purpose: "ingest", IdempotencyKey: ik})
	wantProblem(t, err, 409, "")

	// Durable source checkpoint: written with the batch, read back by source.
	src := uniq("src")
	cty := uniq("RiCkpt")
	if _, err := ing.Bulk(ctx, cty, []map[string]any{{"_pk": "k1"}}, &relata.BulkOptions{Purpose: "ingest", Durability: "sync", SourceCheckpoint: &relata.SourceCheckpoint{Source: src, Position: "offset-42"}}); err != nil {
		t.Fatalf("bulk with checkpoint: %v", err)
	}
	cp, err := ctl.Checkpoint(ctx, src)
	must(t, err, "checkpoint")
	if !strings.Contains(str(cp), "offset-42") {
		t.Fatalf("checkpoint position not durable: %v", cp)
	}
	_, err = ctl.Checkpoint(ctx, uniq("never"))
	wantProblem(t, err, 404, "")
}

// Files journey: folders, put/get/head/list/delete, media content, and both
// the one-shot and the resumable chunked upload with a checksum.
func TestRealJourneyFiles(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	f := relata.NewFilesClient(c)
	dir := uniq("fdir")
	if _, err := f.CreateFolder(ctx, dir+"/", nil); err != nil {
		t.Errorf("create folder: %v", err)
	}
	key := dir + "/a.txt"
	meta, err := f.Put(ctx, key, []byte("hello files"), &relata.FilesOptions{ContentType: "text/plain"})
	must(t, err, "put")
	if meta.Key != key {
		t.Fatalf("put meta: %+v", meta)
	}
	lst, err := f.List(ctx, &relata.ListFilesOptions{Prefix: dir + "/", Limit: 10})
	must(t, err, "list")
	if !strings.Contains(str(lst), "a.txt") {
		t.Fatalf("list missing the object: %v", lst)
	}

	// Resumable upload: manual protocol with status and a partial resume.
	big := bytes.Repeat([]byte("0123456789abcdef"), 1024) // 16 KiB
	sum := sha256.Sum256(big)
	sess, err := f.StartUpload(ctx, dir+"/big.bin", int64(len(big)), &relata.UploadOptions{ContentType: "application/octet-stream", SHA256: hex.EncodeToString(sum[:])})
	must(t, err, "start upload")
	half := int64(len(big) / 2)
	s1, err := f.UploadChunk(ctx, sess.UploadID, 0, big[:half])
	must(t, err, "chunk 1")
	if s1.Offset != half {
		t.Fatalf("offset after chunk1 = %d, want %d", s1.Offset, half)
	}
	stt, err := f.UploadStatus(ctx, sess.UploadID)
	must(t, err, "status")
	if stt.Offset != half || stt.Complete {
		t.Fatalf("status mid-upload: %+v", stt)
	}
	if _, err := f.UploadChunk(ctx, sess.UploadID, half, big[half:]); err != nil {
		t.Fatalf("chunk 2: %v", err)
	}
	done, err := f.CompleteUpload(ctx, sess.UploadID)
	must(t, err, "complete")
	if done.Size != int64(len(big)) {
		t.Fatalf("completed size: %+v", done)
	}
	got, err := f.Get(ctx, dir+"/big.bin", nil)
	must(t, err, "get big")
	if !bytes.Equal(got.Bytes, big) {
		t.Fatalf("resumed upload bytes differ (%d vs %d)", len(got.Bytes), len(big))
	}
	// Abort path.
	sess2, err := f.StartUpload(ctx, dir+"/abort.bin", 10, nil)
	must(t, err, "start 2")
	if err := f.AbortUpload(ctx, sess2.UploadID); err != nil {
		t.Errorf("abort: %v", err)
	}
	_, err = f.UploadStatus(ctx, sess2.UploadID)
	wantProblemAny(t, err)

	// High level Upload with chunking.
	body := bytes.Repeat([]byte("xyz"), 5000)
	meta2, err := f.Upload(ctx, dir+"/auto.bin", bytes.NewReader(body), int64(len(body)), &relata.UploadOptions{ChunkSize: 4096})
	must(t, err, "upload")
	if meta2.Size != int64(len(body)) {
		t.Fatalf("upload meta: %+v", meta2)
	}
	if mc, err := f.MediaContent(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Errorf("media content for an unknown row must fail, got %+v", mc)
	} else {
		wantProblemAny(t, err)
	}
	for _, k := range []string{key, dir + "/big.bin", dir + "/auto.bin"} {
		if err := f.Delete(ctx, k, nil); err != nil {
			t.Errorf("delete %s: %v", k, err)
		}
	}
	_, err = f.Get(ctx, key, nil)
	wantProblem(t, err, 404, "")
}
