package relata

import "testing"

// The server grammar is `FROM <Type> [AS OF '<ts>'] … WHERE … LIMIT n
// [WITH PROVENANCE]`; a live server rejects AS OF after WHERE and WITH
// PROVENANCE before WHERE/LIMIT (found by the real-use Go SDK suite).
func TestQueryBuilderClauseOrderMatchesServerGrammar(t *testing.T) {
	got := NewQuery("SELECT * FROM Person").Where("age > 18").AsOf("2025-01-01").WithProvenance().Limit(5).SQL()
	want := "SELECT * FROM Person AS OF '2025-01-01' WHERE age > 18 LIMIT 5 WITH PROVENANCE"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// A table function base has no plain FROM <Type>: AS OF stays trailing.
	if got := NewQuery("SELECT * FROM PATHS_BETWEEN('a','b')").AsOf("2025-01-01").SQL(); got != "SELECT * FROM PATHS_BETWEEN('a','b') AS OF '2025-01-01'" {
		t.Fatalf("table-function base: %q", got)
	}
	// Existing WHERE in the base still ANDs; AS OF still lands right after FROM <Type>.
	if got := NewQuery("SELECT * FROM T WHERE a = 1").Where("b = 2").AsOf("x").SQL(); got != "SELECT * FROM T AS OF 'x' WHERE a = 1 AND b = 2" {
		t.Fatalf("existing where: %q", got)
	}
}

func TestGraphTraverseDirectionVocabulary(t *testing.T) {
	for in, want := range map[string]string{"out": "outbound", "in": "inbound", "both": "any", "any": "any"} {
		if got := graphTraverseDirections[in]; got != want {
			t.Fatalf("%s -> %q, want %q", in, got, want)
		}
	}
}
