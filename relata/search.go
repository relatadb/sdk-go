package relata

import "context"

// SearchFilter is a single typed-search filter clause (#1983, #2169).
// Op defaults to "eq" on the server side when empty.
type SearchFilter struct {
	Field string `json:"field"`
	Op    string `json:"op,omitempty"`
	Value any    `json:"value"`
}

// SearchRequest is the typed POST /search body (the governed JSON query door).
// From is the object type; Text is BM25 shorthand (compiles to
// rank_by=["bm25", MatchColumn, Text]); RankBy overrides Text with an explicit
// directive (["bm25"|"text", column, query] or ["vector","ann", query]).
type SearchRequest struct {
	From              string         `json:"from"`
	RankBy            []any          `json:"rank_by,omitempty"`
	Filters           []SearchFilter `json:"filters,omitempty"`
	IncludeAttributes []string       `json:"include_attributes,omitempty"`
	Consistency       string         `json:"consistency,omitempty"`
	Limit             int            `json:"limit,omitempty"`
	// BM25 shorthand — set Text (and optionally MatchColumn) instead of RankBy.
	Text        string `json:"-"`
	MatchColumn string `json:"-"`
}

// Search runs a typed search against req.From (an object type) through the
// governed /search door and returns the governed query result. PURPOSE / ACL /
// cell-masking / tenant isolation apply identically to a hand-written query —
// no SQL is constructed on the client side.
//
//	result, err := client.Search(ctx, relata.SearchRequest{
//	    From: "Document", Text: "fraud pattern", Limit: 20,
//	    Filters: []relata.SearchFilter{{Field: "status", Op: "eq", Value: "open"}},
//	})
func (c *Client) Search(ctx context.Context, req SearchRequest) (*QueryResult, error) {
	if req.Limit == 0 {
		req.Limit = 20
	}
	body := struct {
		From              string         `json:"from"`
		RankBy            []any          `json:"rank_by,omitempty"`
		Filters           []SearchFilter `json:"filters,omitempty"`
		IncludeAttributes []string       `json:"include_attributes,omitempty"`
		Consistency       string         `json:"consistency,omitempty"`
		Limit             int            `json:"limit"`
	}{
		From:              req.From,
		RankBy:            req.RankBy,
		Filters:           req.Filters,
		IncludeAttributes: req.IncludeAttributes,
		Consistency:       req.Consistency,
		Limit:             req.Limit,
	}
	if len(body.RankBy) == 0 && req.Text != "" {
		col := req.MatchColumn
		if col == "" {
			col = "*"
		}
		body.RankBy = []any{"bm25", col, req.Text}
	}
	var qr QueryResult
	if err := c.postJSON(ctx, "/search", body, &qr); err != nil {
		return nil, err
	}
	return &qr, nil
}
