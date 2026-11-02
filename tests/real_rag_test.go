package tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

type ragFixture struct {
	Documents []map[string]any `json:"documents"`
	Chunks    []struct {
		ID, DocumentID, Text, SectionPath string
		PageStart, PageEnd                int
	} `json:"-"`
	RawChunks []map[string]any `json:"chunks"`
}

func loadRagFixture(t *testing.T) (docs, chunks []map[string]any, queries []struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Relevant []string `json:"relevant_chunk_ids"`
}) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "docs", "testing", "rag-fixtures")
	rb, err := os.ReadFile(filepath.Join(root, "corpus.json"))
	must(t, err, "read corpus")
	var corpus ragFixture
	must(t, json.Unmarshal(rb, &corpus), "parse corpus")
	qb, err := os.ReadFile(filepath.Join(root, "queries.json"))
	must(t, err, "read queries")
	var q struct {
		Queries []struct {
			ID       string   `json:"id"`
			Text     string   `json:"text"`
			Relevant []string `json:"relevant_chunk_ids"`
		} `json:"queries"`
	}
	must(t, json.Unmarshal(qb, &q), "parse queries")
	return corpus.Documents, corpus.RawChunks, q.Queries
}

// RAG journey: seed the labelled fixture corpus, retrieve with evidence and
// citations, run the full answer chain (smart routing, agentic loop,
// synthesis with resolved citations, stored reasoning trace) and the rank/RRF
// helpers on real hits. The LLM is the caller-supplied function the SDK
// contract defines; everything else is the live server.
func TestRealJourneyRAG(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	_, chunks, queries := loadRagFixture(t)
	run := uniq("rag")
	var rows []map[string]any
	for _, ch := range chunks {
		id := run + "-" + str(ch["id"])
		rows = append(rows, map[string]any{
			"_type": "DocumentChunk", "_pk": id, "chunk_id": id, "report_id": run + "-" + str(ch["document_id"]),
			"sequence_index": 0, "text_body": ch["text"], "section_path": []string{str(ch["section_path"])},
			"page_start": ch["page_start"], "page_end": ch["page_end"], "confidence": 1.0, "extraction_method": "sdk-real:fixture",
		})
	}
	rc, err := c.IngestDocument(ctx, rows, "investigation")
	must(t, err, "seed corpus")
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		st, err := c.IngestDocumentStatus(ctx, rc.TaskID)
		must(t, err, "seed status")
		if st.Status == "complete" {
			if st.RowsWritten != len(rows) {
				t.Fatalf("seed wrote %d of %d", st.RowsWritten, len(rows))
			}
			break
		}
	}
	rag := relata.NewRagClient(c)
	hitRank := func(q string, want string) (int, *relata.RagQueryResponse) {
		var resp *relata.RagQueryResponse
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
			resp, err = rag.Query(ctx, relata.RagQueryRequest{Query: q, Type: "DocumentChunk", TopK: 10, Purpose: "investigation"})
			must(t, err, "rag query")
			for i, h := range resp.Hits {
				if h.ChunkID == want {
					return i, resp
				}
			}
		}
		return -1, resp
	}
	correct := 0
	for _, q := range queries {
		want := run + "-" + q.Relevant[0]
		rank, resp := hitRank(q.Text, want)
		if rank >= 0 {
			correct++
			h := resp.Hits[rank]
			if h.Text == "" || h.ReportID == "" {
				t.Errorf("%s: hit lacks evidence fields: %+v", q.ID, h)
			}
		} else {
			t.Errorf("%s %q: relevant chunk %s not in top 10 (%d hits)", q.ID, q.Text, want, len(resp.Hits))
		}
	}
	t.Logf("recall@10: %d/%d", correct, len(queries))

	// Modes and options on the same corpus.
	for _, mode := range []string{"lexical", "hybrid"} {
		r, err := rag.Query(ctx, relata.RagQueryRequest{Query: "encryption at rest", Type: "DocumentChunk", TopK: 3, SearchMode: mode, Purpose: "investigation", ExpandWindow: true})
		if err != nil || len(r.Hits) == 0 || len(r.Hits) > 3 {
			t.Errorf("mode %s: %v hits=%v", mode, err, r)
		}
	}
	_, err = rag.Query(ctx, relata.RagQueryRequest{Query: "x", Type: "NoSuchType" + run, Purpose: "investigation"})
	t.Logf("unknown type: %v", err)

	// Full answer chain with a caller-supplied LLM that cites what it is given.
	idRe := regexp.MustCompile(`\[(` + regexp.QuoteMeta(run) + `-[A-Za-z0-9_\-]+)\]`)
	llm := func(prompt string) (string, error) {
		m := idRe.FindStringSubmatch(prompt)
		if m == nil {
			return "No evidence was supplied.", nil
		}
		return "The retrieved evidence answers the question [" + m[1] + "].", nil
	}
	embed := relata.NewVectorClient(c)
	mcp := relata.NewMcpClient(c)
	ans, err := relata.RunRagAnswer(ctx, rag, mcp, "When was the company founded?", "DocumentChunk", relata.RunRagAnswerOptions{
		Purpose: "investigation", LLM: llm,
		EmbeddingFn: func(ctx context.Context, text string) ([]float64, error) {
			e, err := embed.Embed(ctx, text, "")
			if err != nil {
				return nil, err
			}
			return e.Embedding, nil
		},
		EntailmentFn: func(string, []string) (bool, error) { return true, nil },
	}.DisableFaithfulnessCheck())
	must(t, err, "run rag answer")
	if ans.SynthesisResult == nil || len(ans.SynthesisResult.Citations) == 0 {
		t.Fatalf("answer has no resolved citations: %+v", ans)
	}
	if !strings.HasPrefix(ans.SynthesisResult.Citations[0].ChunkID, run+"-") || ans.SynthesisResult.Citations[0].ReportID == "" {
		t.Fatalf("citation does not resolve to a retrieved chunk: %+v", ans.SynthesisResult.Citations[0])
	}
	if ans.Trace == nil {
		t.Errorf("reasoning trace was not stored")
	}
	t.Logf("trace keys: %d", len(ans.Trace))

	sm, err := relata.SmartRagQuery(ctx, rag, "What encryption protects data at rest?", "DocumentChunk", relata.SmartRagQueryOptions{Purpose: "investigation"})
	must(t, err, "smart rag")
	if sm.IsRefused() || len(sm.Hits) == 0 {
		t.Fatalf("smart rag: %+v", sm)
	}
	// Rank/fusion helpers on real hits.
	hits := sm.Hits
	r1 := &relata.RagQueryResponse{Hits: hits}
	if len(relata.RrfScores([]*relata.RagQueryResponse{r1, r1}, 60)) == 0 {
		t.Errorf("rrf scores empty")
	}
	if merged := relata.RrfMerge([]*relata.RagQueryResponse{r1, r1}, 60); len(merged.Hits) != len(hits) {
		t.Errorf("rrf merge dropped hits: %d vs %d", len(merged.Hits), len(hits))
	}
	sel, err := relata.MMRSelect(hits, &relata.MMRSelectOptions{})
	if err != nil || len(sel) == 0 {
		t.Errorf("mmr: %v %v", sel, err)
	}
}
