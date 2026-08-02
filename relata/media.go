package relata

import (
	"context"
	"fmt"
	"strconv"
)

// ---------------------------------------------------------------------------
// Multimedia operators (#2251, #2840) — governed wrappers over FACE_SEARCH /
// MATCH_PDQ / SIMILAR_IMAGE. All three route through POST /query so PURPOSE /
// ACL / cell-masking / tenant isolation apply identically to a hand-written
// query. The server operators are registered in relata_query::parser:
//   - FACE_SEARCH('<csv floats>', '<gallery>', K => n, THRESHOLD => f)
//   - MATCH_PDQ('<hash>', '<corpus>', THRESHOLD => f)
//   - SIMILAR_IMAGE('<media_ref>', THRESHOLD => f, INDEX => '<corpus>')
// ---------------------------------------------------------------------------

// sqlLiteral quotes a string as a SQL literal, doubling internal quotes.
func sqlLiteral(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}

// floatsToCSV renders a numeric slice ([]float32 / []float64) as a
// comma-separated string. Non-slice values fall back to fmt.Sprint.
func floatsToCSV(v any) string {
	switch vs := v.(type) {
	case []float32:
		out := make([]byte, 0, len(vs)*8)
		for i, f := range vs {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, strconv.FormatFloat(float64(f), 'f', -1, 32)...)
		}
		return string(out)
	case []float64:
		out := make([]byte, 0, len(vs)*8)
		for i, f := range vs {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, strconv.FormatFloat(f, 'f', -1, 64)...)
		}
		return string(out)
	default:
		return fmt.Sprint(v)
	}
}

func itoa(n int) string   { return strconv.Itoa(n) }
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// FaceSearchOption configures a FaceSearch call.
type FaceSearchOption func(*faceSearchParams)

type faceSearchParams struct {
	k          int
	threshold  float64
	purpose    string
	timeoutSet bool
}

// WithFaceK sets the max number of candidates returned (default 10).
func WithFaceK(k int) FaceSearchOption {
	return func(p *faceSearchParams) { p.k = k }
}

// WithFaceThreshold sets the minimum cosine similarity 0–1 (default 0.7).
func WithFaceThreshold(t float64) FaceSearchOption {
	return func(p *faceSearchParams) { p.threshold = t }
}

// WithFacePurpose sets the PURPOSE for the underlying governed query.
func WithFacePurpose(purpose string) FaceSearchOption {
	return func(p *faceSearchParams) { p.purpose = purpose }
}

// buildFaceSearchSQL assembles the SELECT * FROM FACE_SEARCH(...) ticket
// (#2251). Mirrors the server operator in relata_query::parser. Exported for
// testing parity with the TypeScript buildFaceSearchSql helper.
func buildFaceSearchSQL(galleryID, csv string, k int, threshold float64) string {
	return "SELECT * FROM FACE_SEARCH(" +
		sqlLiteral(csv) + ", " + sqlLiteral(galleryID) +
		", K => " + itoa(k) + ", THRESHOLD => " + ftoa(threshold) + ")"
}

// FaceSearch runs a biometric face k-NN search against a gallery (#2251,
// ADR-030). It executes SELECT * FROM FACE_SEARCH(...) through the governed
// /query door.
//
// embedding may be a []float32, []float64, or a pre-formatted comma-separated
// string. k defaults to 10, threshold to 0.7.
func (c *Client) FaceSearch(ctx context.Context, galleryID string, embedding any, opts ...FaceSearchOption) (*QueryResult, error) {
	p := &faceSearchParams{k: 10, threshold: 0.7}
	for _, o := range opts {
		o(p)
	}
	csv, ok := embedding.(string)
	if !ok {
		csv = floatsToCSV(embedding)
	}
	sql := buildFaceSearchSQL(galleryID, csv, p.k, p.threshold)
	queryOpts := []QueryOption{}
	if p.purpose != "" {
		queryOpts = append(queryOpts, WithPurpose(p.purpose))
	}
	return c.Query(ctx, sql, queryOpts...)
}

// MatchPdqOption configures a MatchPdq call.
type MatchPdqOption func(*matchPdqParams)

type matchPdqParams struct {
	threshold float64
	purpose   string
}

// WithPdqThreshold sets the minimum Hamming similarity 0–1 (default 0.9).
func WithPdqThreshold(t float64) MatchPdqOption {
	return func(p *matchPdqParams) { p.threshold = t }
}

// WithPdqPurpose sets the PURPOSE for the underlying governed query.
func WithPdqPurpose(purpose string) MatchPdqOption {
	return func(p *matchPdqParams) { p.purpose = purpose }
}

// buildMatchPdqSQL assembles the SELECT * FROM MATCH_PDQ(...) ticket (#2251).
// Mirrors the TypeScript buildMatchPdqSql helper.
func buildMatchPdqSQL(corpusID, queryHash string, threshold float64) string {
	return "SELECT * FROM MATCH_PDQ(" +
		sqlLiteral(queryHash) + ", " + sqlLiteral(corpusID) +
		", THRESHOLD => " + ftoa(threshold) + ")"
}

// MatchPdq runs a perceptual-hash (PDQ) near-duplicate search over a corpus
// (#2251). It executes SELECT * FROM MATCH_PDQ(...) through the governed /query
// door. PDQ near-duplicates differ by ≤ 31 bits (ADR-187). threshold defaults
// to 0.9.
func (c *Client) MatchPdq(ctx context.Context, corpusID, queryHash string, opts ...MatchPdqOption) (*QueryResult, error) {
	p := &matchPdqParams{threshold: 0.9}
	for _, o := range opts {
		o(p)
	}
	sql := buildMatchPdqSQL(corpusID, queryHash, p.threshold)
	queryOpts := []QueryOption{}
	if p.purpose != "" {
		queryOpts = append(queryOpts, WithPurpose(p.purpose))
	}
	return c.Query(ctx, sql, queryOpts...)
}

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

// buildSimilarImageSQL assembles the SELECT * FROM SIMILAR_IMAGE(...) ticket
// (#2840, PR #2859). Mirrors the server operator's grammar in
// relata_query::parser (SIMILAR_IMAGE's parser arm): a positional media_ref
// literal followed by optional named THRESHOLD / INDEX arguments.
func buildSimilarImageSQL(mediaRef string, threshold float64, index string) string {
	sql := "SELECT * FROM SIMILAR_IMAGE(" +
		sqlLiteral(mediaRef) + ", THRESHOLD => " + ftoa(threshold)
	if index != "" {
		sql += ", INDEX => " + sqlLiteral(index)
	}
	return sql + ")"
}

// SimilarImage runs a perceptual near-duplicate image search over mediaRef
// (#2840, PR #2859) via the server's SIMILAR_IMAGE operator. It executes
// SELECT * FROM SIMILAR_IMAGE(...) through the governed /query door so
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
