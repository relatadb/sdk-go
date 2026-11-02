package tests

import (
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// Vector journey: embed through the server, store chunks with embeddings,
// then nearest-neighbour, hybrid and similar-to retrieval on real vectors.
func TestRealJourneyVectors(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	v := relata.NewVectorClient(c)
	texts := []string{
		"Quarterly revenue grew because enterprise subscriptions renewed early.",
		"The firewall blocked repeated login failures from an unfamiliar subnet.",
		"Penguins huddle together to survive the Antarctic winter storms.",
	}
	one, err := v.Embed(ctx, texts[0], "")
	must(t, err, "embed")
	if one.Dim == 0 || len(one.Embedding) != one.Dim {
		t.Fatalf("embed: dim=%d len=%d", one.Dim, len(one.Embedding))
	}
	batch, err := v.EmbedBatch(ctx, texts, "")
	must(t, err, "embed batch")
	if batch.Count != 3 || len(batch.Embeddings) != 3 || len(batch.Embeddings[1]) != one.Dim {
		t.Fatalf("embed batch: %+v", batch)
	}
	// Determinism: the same text embeds to the same vector.
	again, _ := v.Embed(ctx, texts[0], "")
	for i := range one.Embedding {
		if one.Embedding[i] != again.Embedding[i] {
			t.Fatalf("embedding is not deterministic at %d", i)
		}
	}

	run := uniq("vec")
	var rows []map[string]any
	for i, tx := range texts {
		id := run + "-" + string(rune('a'+i))
		rows = append(rows, map[string]any{"_type": "DocumentChunk", "_pk": id, "chunk_id": id, "report_id": run, "sequence_index": i, "text_body": tx, "section_path": []string{"s"}, "page_start": 1, "page_end": 1, "confidence": 1.0, "extraction_method": "sdk-real", "_emb_text": batch.Embeddings[i]})
	}
	rc, err := c.IngestDocument(ctx, rows, "investigation")
	must(t, err, "ingest vectors")
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if s, _ := c.IngestDocumentStatus(ctx, rc.TaskID); s != nil && s.Status == "complete" {
			break
		}
	}
	q, err := v.Embed(ctx, "login failures blocked by the firewall", "")
	must(t, err, "embed query")
	var hits []map[string]any
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		hits, err = v.KNNSearch(ctx, "DocumentChunk", "_emb_text", q.Embedding, 3, &relata.KNNOptions{Purpose: "investigation"})
		must(t, err, "knn")
		if len(hits) > 0 && strings.Contains(str(hits[0]), run) {
			break
		}
	}
	if len(hits) == 0 || !strings.Contains(str(hits[0]["chunk_id"]), run+"-b") {
		t.Fatalf("nearest neighbour of the firewall query must be the firewall chunk: %v", hits)
	}
	var hy []map[string]any
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		hy, err = v.HybridSearch(ctx, "DocumentChunk", "firewall login "+run, &relata.HybridSearchOptions{K: 5, Purpose: "investigation", Metric: "cosine", Weights: &[3]float64{0, 1, 1}})
		must(t, err, "hybrid")
		if len(hy) > 0 {
			break
		}
	}
	if len(hy) == 0 {
		t.Errorf("hybrid search returned nothing for text that was just ingested")
	}
	_, err = v.HybridSearch(ctx, "DocumentChunk", "x", &relata.HybridSearchOptions{Metric: "bogus"})
	if err == nil {
		t.Errorf("an unknown metric must be refused client-side")
	}
	if st, err := v.SimilarTo(ctx, "DocumentChunk", run+"-b", &relata.SimilarToOptions{K: 2, Purpose: "investigation"}); err != nil {
		t.Logf("similar to: %v", err)
	} else {
		t.Logf("similar to: %d rows", len(st))
	}
}
