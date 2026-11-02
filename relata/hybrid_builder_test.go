package relata

import "testing"

func TestHybridSearchBuilderEmitsParsableStatement(t *testing.T) {
	b, err := HybridSearch("Doc", "o'brien ring", 7)
	if err != nil {
		t.Fatal(err)
	}
	want := "HYBRID_SEARCH FROM Doc QUERY 'o\\'brien ring' LIMIT 7"
	if got := b.SQL(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
