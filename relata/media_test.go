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

// TestBuildSQLNeverWrapsOperatorInSelectFrom pins #5497: the engine only
// parses the bare top-level OPERATOR(...) form — a SELECT * FROM wrapper
// parses the operator name as a table identifier and fails on the trailing
// "(" (mirrors the TS SDK's #5496 and the Python SDK's #5497 regression
// tests).
func TestBuildSQLNeverWrapsOperatorInSelectFrom(t *testing.T) {
	for name, sql := range map[string]string{
		"SIMILAR_IMAGE": buildSimilarImageSQL("m", 0.9, ""),
	} {
		if strings.Contains(sql, "SELECT") {
			t.Errorf("%s SQL must be the bare operator form, got %q", name, sql)
		}
	}
}
