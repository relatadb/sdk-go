package relata

import (
	"strings"
	"testing"
)

// TestSQLLiteral verifies backslash escaping of `\` and `'` — the injection
// guard for the multimedia operators (#2251, #3211). The server lexer
// terminates a literal at the first unescaped quote and honours backslash
// escapes, so both characters must be backslash-escaped.
func TestSQLLiteral(t *testing.T) {
	cases := map[string]string{
		"abc":         "'abc'",
		"a'b":         `'a\'b'`,
		`a\b`:         `'a\\b'`,
		"0.1,0.2,0.3": "'0.1,0.2,0.3'",
		`has"quote`:   `'has"quote'`,
	}
	for in, want := range cases {
		if got := sqlLiteral(in); got != want {
			t.Errorf("sqlLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFloatsToCSV verifies the embedding-serialisation paths used by FaceSearch.
func TestFloatsToCSV(t *testing.T) {
	if got := floatsToCSV([]float32{0.1, 0.2, 0.3}); got != "0.1,0.2,0.3" {
		t.Errorf("float32 slice: got %q", got)
	}
	if got := floatsToCSV([]float64{0.5}); got != "0.5" {
		t.Errorf("float64 single: got %q", got)
	}
	if got := floatsToCSV("already,csv"); got != "already,csv" {
		t.Errorf("passthrough fallback: got %q", got)
	}
}

// TestBuildFaceSearchSQL pins the exact SQL shape the governed /query door
// expects, matching the server operator in relata_query::parser and the
// TypeScript buildFaceSearchSql helper.
func TestBuildFaceSearchSQL(t *testing.T) {
	got := buildFaceSearchSQL("gallery-1", "0.1,0.2,0.3", 5, 0.6)
	want := "FACE_SEARCH('0.1,0.2,0.3', 'gallery-1', K => 5, THRESHOLD => 0.6)"
	if got != want {
		t.Errorf("buildFaceSearchSQL:\n got %q\nwant %q", got, want)
	}
}

// TestBuildFaceSearchSQL_QuoteInjection ensures a malicious gallery id can't
// break out of the SQL literal (#3211): the quote is backslash-escaped so the
// payload stays contained in a single string literal.
func TestBuildFaceSearchSQL_QuoteInjection(t *testing.T) {
	got := buildFaceSearchSQL("x'); DROP TABLE y;--", "1", 1, 0.5)
	want := `FACE_SEARCH('1', 'x\'); DROP TABLE y;--', K => 1, THRESHOLD => 0.5)`
	if got != want {
		t.Errorf("quote injection not neutralised:\n got %q\nwant %q", got, want)
	}
}

// TestBuildMatchPdqSQL pins the MATCH_PDQ shape (ADR-187 PDQ near-duplicate
// search), matching the TypeScript buildMatchPdqSql helper.
func TestBuildMatchPdqSQL(t *testing.T) {
	got := buildMatchPdqSQL("corpus-7", "abcd1234", 0.9)
	want := "MATCH_PDQ('abcd1234', 'corpus-7', THRESHOLD => 0.9)"
	if got != want {
		t.Errorf("buildMatchPdqSQL:\n got %q\nwant %q", got, want)
	}
}

// TestBuildSQLNeverWrapsOperatorInSelectFrom pins #5497: the engine only
// parses the bare top-level OPERATOR(...) form — a SELECT * FROM wrapper
// parses the operator name as a table identifier and fails on the trailing
// "(" (mirrors the TS SDK's #5496 and the Python SDK's #5497 regression
// tests).
func TestBuildSQLNeverWrapsOperatorInSelectFrom(t *testing.T) {
	for name, sql := range map[string]string{
		"FACE_SEARCH":   buildFaceSearchSQL("g", "0.1", 10, 0.7),
		"MATCH_PDQ":     buildMatchPdqSQL("c", "ff", 0.9),
		"SIMILAR_IMAGE": buildSimilarImageSQL("m", 0.9, ""),
	} {
		if strings.Contains(sql, "SELECT") {
			t.Errorf("%s SQL must be the bare operator form, got %q", name, sql)
		}
	}
}
