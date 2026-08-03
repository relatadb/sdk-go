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
// directive (["bm25"|"text", column, query], ["field_weight",
// map[string]float64{"title": 3.0, ...}, query] — BM25F per-field weighting,
// compiles to MATCH(*, ...) + RANK BY FIELD_WEIGHT(...), #2676 — or
// ["vector","ann", query]).
type SearchRequest struct {
	From              string         `json:"from"`
	RankBy            []any          `json:"rank_by,omitempty"`
	Filters           []SearchFilter `json:"filters,omitempty"`
	IncludeAttributes []string       `json:"include_attributes,omitempty"`
	Consistency       string         `json:"consistency,omitempty"`
	Limit             int            `json:"limit,omitempty"`
	// ComputeAttributes carries side-output ranking signals per hit,
	// label -> expr (e.g. {"bm25_score": "BM25()"}) — compiles to a trailing
	// COMPUTE clause (#1985 T3, #2465). Purely additive: never affects which
	// rows match or their order.
	ComputeAttributes map[string]string `json:"compute_attributes,omitempty"`
	// BM25 shorthand — set Text (and optionally MatchColumn) instead of RankBy.
	Text        string `json:"-"`
	MatchColumn string `json:"-"`
}

// SearchClient is the typed client for the `/search` JSON query door (#1983,
// #2169). It is a separate type from *Client because the untyped
// [Client.Search] (query/objectType/opts, #670) already owns that method
// name; this mirrors the Python/TS SDKs, which likewise expose the typed
// door through a dedicated `SearchClient`, not a `Client` method.
type SearchClient struct {
	c *Client
}

// NewSearchClient constructs a SearchClient that backs onto the parent
// client's auth, tenant, and purpose context.
func NewSearchClient(c *Client) *SearchClient {
	return &SearchClient{c: c}
}

// Query runs a typed search against req.From (an object type) through the
// governed /search door and returns the governed query result. PURPOSE / ACL /
// cell-masking / tenant isolation apply identically to a hand-written query —
// no SQL is constructed on the client side.
//
//	search := relata.NewSearchClient(client)
//	result, err := search.Query(ctx, relata.SearchRequest{
//	    From: "Document", Text: "fraud pattern", Limit: 20,
//	    Filters: []relata.SearchFilter{{Field: "status", Op: "eq", Value: "open"}},
//	})
func (s *SearchClient) Query(ctx context.Context, req SearchRequest) (*QueryResult, error) {
	if req.Limit == 0 {
		req.Limit = 20
	}
	body := struct {
		From              string            `json:"from"`
		RankBy            []any             `json:"rank_by,omitempty"`
		Filters           []SearchFilter    `json:"filters,omitempty"`
		IncludeAttributes []string          `json:"include_attributes,omitempty"`
		Consistency       string            `json:"consistency,omitempty"`
		Limit             int               `json:"limit"`
		ComputeAttributes map[string]string `json:"compute_attributes,omitempty"`
	}{
		From:              req.From,
		RankBy:            req.RankBy,
		Filters:           req.Filters,
		IncludeAttributes: req.IncludeAttributes,
		Consistency:       req.Consistency,
		Limit:             req.Limit,
		ComputeAttributes: req.ComputeAttributes,
	}
	if len(body.RankBy) == 0 && req.Text != "" {
		col := req.MatchColumn
		if col == "" {
			col = "*"
		}
		body.RankBy = []any{"bm25", col, req.Text}
	}
	var qr QueryResult
	if err := s.c.postJSON(ctx, "/search", body, &qr); err != nil {
		return nil, err
	}
	return &qr, nil
}
