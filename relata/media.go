package relata

import (
	"context"
	"strconv"
)

// ---------------------------------------------------------------------------
// Multimedia operators (#2251, #2840) — governed wrapper over SIMILAR_IMAGE
// (FACE_SEARCH / MATCH_PDQ were retired from core, #5935). It routes through POST /query so PURPOSE /
// ACL / cell-masking / tenant isolation apply identically to a hand-written
// query. The server operators are registered in relata_query::parser:
//   - SIMILAR_IMAGE('<media_ref>', THRESHOLD => f, INDEX => '<corpus>')
// ---------------------------------------------------------------------------

// sqlLiteral quotes a string as a SQL literal, backslash-escaping `\` and `'`
// so the value cannot break out of the literal (#3211). See escapeSQLString.
func sqlLiteral(s string) string {
	return "'" + escapeSQLString(s) + "'"
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// SimilarImageOption configures a SimilarImage call.
type SimilarImageOption func(*similarImageParams)

type similarImageParams struct {
	threshold float64
	index     string
	purpose   string
}

// WithSimilarImageThreshold sets the minimum similarity 0–1 (default 0.9).
func WithSimilarImageThreshold(t float64) SimilarImageOption {
	return func(p *similarImageParams) { p.threshold = t }
}

// WithSimilarImageIndex scopes the search to a named corpus/index. Omitted
// when empty, matching the server's optional INDEX => '<corpus>' argument.
func WithSimilarImageIndex(index string) SimilarImageOption {
	return func(p *similarImageParams) { p.index = index }
}

// WithSimilarImagePurpose sets the PURPOSE for the underlying governed query.
func WithSimilarImagePurpose(purpose string) SimilarImageOption {
	return func(p *similarImageParams) { p.purpose = purpose }
}

// buildSimilarImageSQL assembles the bare SIMILAR_IMAGE(...) ticket
// (#2840, PR #2859, #5497). Bare top-level form only — the
// engine only dispatches it as a bare top-level statement (#5497). Mirrors the server operator's grammar in
// relata_query::parser (SIMILAR_IMAGE's parser arm): a positional media_ref
// literal followed by optional named THRESHOLD / INDEX arguments.
func buildSimilarImageSQL(mediaRef string, threshold float64, index string) string {
	sql := "SIMILAR_IMAGE(" +
		sqlLiteral(mediaRef) + ", THRESHOLD => " + ftoa(threshold)
	if index != "" {
		sql += ", INDEX => " + sqlLiteral(index)
	}
	return sql + ")"
}

// SimilarImage runs a perceptual near-duplicate image search over mediaRef
// (#2840, PR #2859) via the server's SIMILAR_IMAGE operator. It executes
// SIMILAR_IMAGE(...) through the governed /query door so
// PURPOSE / ACL / cell-masking / tenant isolation apply identically to a
// hand-written query. threshold defaults to 0.9; when no index is supplied
// the server searches its default scope.
func (c *Client) SimilarImage(ctx context.Context, mediaRef string, opts ...SimilarImageOption) (*QueryResult, error) {
	p := &similarImageParams{threshold: 0.9}
	for _, o := range opts {
		o(p)
	}
	sql := buildSimilarImageSQL(mediaRef, p.threshold, p.index)
	queryOpts := []QueryOption{}
	if p.purpose != "" {
		queryOpts = append(queryOpts, WithPurpose(p.purpose))
	}
	return c.Query(ctx, sql, queryOpts...)
}
