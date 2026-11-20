package relata

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// #5726: results expose the query-embedding flag and warn when it is not semantic.
func TestQueryResultEmbeddingFlagFallbackWarns(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	body := `{"rows":0,"data":[],"embedding":{"mode":"lexical-fallback","semantic":false,` +
		`"model":"cpu-deterministic:v1","reason":"no endpoint"}}`
	var r QueryResult
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.Embedding == nil || r.Embedding.Mode != "lexical-fallback" || r.Embedding.Semantic {
		t.Fatalf("flag not decoded: %+v", r.Embedding)
	}
	if r.Embedding.Reason == nil || *r.Embedding.Reason != "no endpoint" {
		t.Fatalf("reason not decoded: %+v", r.Embedding)
	}
	if !strings.Contains(buf.String(), "lexical fallback") {
		t.Fatalf("no warning logged: %q", buf.String())
	}
}

func TestEmbeddingFlagFromMapSemanticDoesNotWarn(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	f := embeddingFlagFromMap(map[string]any{
		"embedding": map[string]any{"mode": "semantic", "semantic": true, "model": "e5:1", "reason": nil},
	})
	if f == nil || !f.Semantic || f.Model == nil || *f.Model != "e5:1" || f.Reason != nil {
		t.Fatalf("bad flag: %+v", f)
	}
	warnIfLexical(f)
	if buf.Len() != 0 {
		t.Fatalf("semantic flag must not warn: %q", buf.String())
	}
	var rag RagQueryResponse
	mismatch := `{"hits":[],"embedding":{"mode":"lexical-fallback","semantic":false,"model":null,"reason":"model mismatch"}}`
	if err := json.Unmarshal([]byte(mismatch), &rag); err != nil {
		t.Fatal(err)
	}
	if rag.Embedding == nil || *rag.Embedding.Reason != "model mismatch" {
		t.Fatalf("rag flag not decoded: %+v", rag.Embedding)
	}
}
