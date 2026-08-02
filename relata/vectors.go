package relata

import (
	"context"
	"encoding/json"
	"fmt"
)

// VectorClient is the synchronous typed vector / hybrid-search client. The
// server does not expose dedicated /similar or /hybrid_search HTTP routes
// today — vector search is reachable via the HYBRID_SEARCH and SIMILAR TO SQL
// operators. This client wraps those operators so callers do not have to
// hand-build SQL.
type VectorClient struct {
	c *Client
}

// NewVectorClient constructs a VectorClient that backs onto the parent
// client's Query path. Mirrors the Python reference's VectorClient.from_client.
func NewVectorClient(c *Client) *VectorClient {
	return &VectorClient{c: c}
}

// VectorPurposeOptions carries a per-call purpose override.
type VectorPurposeOptions struct {
	// Purpose is the declared purpose token. Falls back to the parent client's
	// DefaultPurpose when empty.
	Purpose string
}

// KNNOptions configures the optional KNN search parameters.
type KNNOptions struct {
	// Purpose is the declared purpose token (see VectorPurposeOptions).
	Purpose string
	// EFSearch is the HNSW beam width hint. There is no server-side wire
	// knob for it today (see KNNSearch's doc comment, #2756); when > 0 it
	// is appended to the outgoing SQL as a "/* EF_SEARCH n */" comment
	// rather than silently discarded.
	EFSearch int
}

// HybridSearchOptions configures HybridSearch.
type HybridSearchOptions struct {
	// Purpose is the declared purpose token.
	Purpose string
	// K caps the result count (default 10).
	K int
	// Rerank re-scores the top-K via the sidecar cross-encoder (#611).
	Rerank bool
	// Metric is the distance metric for the vector channel (#1330), e.g.
	// "cosine", "l2", "dotproduct". Empty leaves the server default.
	Metric string
	// Weights optionally overrides per-query fusion weights [graph, bm25,
	// vector] (#1338). Nil leaves the server's default fusion.
	Weights *[3]float64
}

// SimilarToOptions configures SimilarTo.
type SimilarToOptions struct {
	// Purpose is the declared purpose token.
	Purpose string
	// K caps the result count (default 10).
	K int
}

// KNNSearch performs a pure KNN search over a named embedding slot. Emits
// "SELECT * FROM <Type> ORDER BY <slot> <=> '[...]' LIMIT k" — the pgvector
// cosine form the server understands natively.
//
// opts.EFSearch (HNSW beam width): investigated for #2756 — there is no
// client-controllable ef_search knob anywhere in today's wire contract.
// relata_query::parser's ORDER BY clause only accepts "column [ASC|DESC]"
// (no trailing-clause grammar like HYBRID_SEARCH's RERANK/METRIC/WEIGHTS),
// and neither the HTTP nor gRPC QueryRequest carries an ef_search field.
// ef_search is exclusively an internal, auto-tuned HNSW parameter
// (relata-storage's HnswIndex::ef_search_default autotune loop) with no
// per-query override path today. Rather than silently discarding a
// caller-supplied value (the previous, dead-parameter behavior), it is
// appended as a "/* EF_SEARCH n */" SQL comment: harmless (the tokenizer
// strips comments before the "no trailing tokens" parse check ever runs, so
// this can never change what executes), visible in the outgoing
// request/query logs instead of vanishing, and ready to be wired to a real
// per-query knob if the server ever exposes one.
func (v *VectorClient) KNNSearch(ctx context.Context, objectType, embeddingSlot string, queryEmbedding []float64, k int, opts *KNNOptions) ([]map[string]any, error) {
	if k <= 0 {
		k = 10
	}
	embStr, err := json.Marshal(queryEmbedding)
	if err != nil {
		return nil, fmt.Errorf("relata: encode embedding: %w", err)
	}
	sql := fmt.Sprintf(
		"SELECT * FROM %s ORDER BY %s <=> '%s' LIMIT %d",
		objectType, embeddingSlot, string(embStr), k,
	)
	purpose := ""
	if opts != nil {
		purpose = opts.Purpose
		if opts.EFSearch > 0 {
			sql += fmt.Sprintf(" /* EF_SEARCH %d */", opts.EFSearch)
		}
	}
	result, err := v.query(ctx, sql, purpose)
	if err != nil {
		return nil, err
	}
	return result.Rows, nil
}

// buildHybridSearchSQL assembles the HYBRID_SEARCH FROM ... QUERY ... LIMIT ...
// statement the server's parser accepts (relata_query::parser), with optional
// RERANK / METRIC / WEIGHTS trailing clauses. The previous
// "SELECT * FROM HYBRID_SEARCH(from => …)" TVF shape was never accepted by the
// server (named args aren't a real grammar). Mirrors the TypeScript
// buildHybridSearchSql / Python _hybrid_search_sql helpers.
func buildHybridSearchSQL(objectType, queryText string, k int, rerank bool, metric string, weights *[3]float64) string {
	if k <= 0 {
		k = 10
	}
	sql := fmt.Sprintf("HYBRID_SEARCH FROM %s QUERY '%s' LIMIT %d",
		objectType, escapeSingleQuotes(queryText), k)
	if rerank {
		sql += " RERANK"
	}
	if metric != "" {
		sql += fmt.Sprintf(" METRIC %s", metric)
	}
	if weights != nil {
		sql += fmt.Sprintf(" WEIGHTS %v %v %v", weights[0], weights[1], weights[2])
	}
	return sql
}

// HybridSearch performs a hybrid BM25 + vector search via the HYBRID_SEARCH
// operator. Executes "HYBRID_SEARCH FROM <type> QUERY '<text>' LIMIT <k>"
// through the governed /query door so PURPOSE / ACL / cell-masking / tenant
// isolation apply identically to a hand-written query. The server embeds
// queryText server-side; caller-supplied embeddings are NOT accepted by the
// /query SQL surface (no vector-literal grammar — use Embed() for text→vector
// instead).
func (v *VectorClient) HybridSearch(ctx context.Context, objectType, queryText string, opts *HybridSearchOptions) ([]map[string]any, error) {
	if queryText == "" {
		return nil, fmt.Errorf("relata: hybrid_search requires query_text")
	}
	k, purpose, rerank, metric := 10, "", false, ""
	var weights *[3]float64
	if opts != nil {
		if opts.K > 0 {
			k = opts.K
		}
		purpose = opts.Purpose
		rerank = opts.Rerank
		metric = opts.Metric
		weights = opts.Weights
	}
	sql := buildHybridSearchSQL(objectType, queryText, k, rerank, metric, weights)
	result, err := v.query(ctx, sql, purpose)
	if err != nil {
		return nil, err
	}
	return result.Rows, nil
}

// SimilarTo performs multi-vector similarity ("SIMILAR TO") — ranks by max-pool
// cosine over every _emb_* slot on the reference row.
func (v *VectorClient) SimilarTo(ctx context.Context, objectType, referenceID string, opts *SimilarToOptions) ([]map[string]any, error) {
	k := 10
	purpose := ""
	if opts != nil {
		if opts.K > 0 {
			k = opts.K
		}
		purpose = opts.Purpose
	}
	sql := fmt.Sprintf(
		"SELECT * FROM SIMILAR TO %s WHERE id = '%s' LIMIT %d",
		objectType, escapeSingleQuotes(referenceID), k,
	)
	result, err := v.query(ctx, sql, purpose)
	if err != nil {
		return nil, err
	}
	return result.Rows, nil
}

// query resolves the effective purpose and runs sql via the parent client.
func (v *VectorClient) query(ctx context.Context, sql, purpose string) (*QueryResult, error) {
	eff := purpose
	if eff == "" {
		eff = v.c.defaultPurpose
	}
	if eff == "" {
		return nil, ErrPurposeRequired
	}
	return v.c.Query(ctx, sql, WithPurpose(eff))
}

// EmbedResponse is the body of POST /embed (#1172).
type EmbedResponse struct {
	Embedding []float64 `json:"embedding"`
	Model     string    `json:"model"`
	Dim       int       `json:"dim"`
}

// EmbedBatchResponse is the body of POST /embed/batch (#1172).
type EmbedBatchResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
	Model      string      `json:"model"`
	Dim        int         `json:"dim"`
	Count      int         `json:"count"`
}

// Embed embeds a single text string via POST /embed. Uses the server's
// built-in CPU lexical embedder when no sidecar is configured, or the GPU
// sidecar (RELATA_ACCEL_ENDPOINT) when set. model is an optional model hint
// passed through to the server; pass "" to omit it.
func (v *VectorClient) Embed(ctx context.Context, text, model string) (*EmbedResponse, error) {
	body := map[string]any{"text": text}
	if model != "" {
		body["model"] = model
	}
	var out EmbedResponse
	if err := v.c.postJSON(ctx, "/embed", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EmbedBatch embeds multiple texts in one call via POST /embed/batch. model
// is an optional model hint passed through to the server; pass "" to omit it.
func (v *VectorClient) EmbedBatch(ctx context.Context, texts []string, model string) (*EmbedBatchResponse, error) {
	body := map[string]any{"texts": texts}
	if model != "" {
		body["model"] = model
	}
	var out EmbedBatchResponse
	if err := v.c.postJSON(ctx, "/embed/batch", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── #2444 (ADR-0276): per-modality data-plane media embed routes ───────────

// embedMedia POSTs {"bytes_b64": ..., "model": ...} to /embed/{modality} and
// decodes the shared {embedding, model, dim} response shape.
func (v *VectorClient) embedMedia(ctx context.Context, modality, bytesB64, model string) (*EmbedResponse, error) {
	body := map[string]any{"bytes_b64": bytesB64}
	if model != "" {
		body["model"] = model
	}
	var out EmbedResponse
	if err := v.c.postJSON(ctx, "/embed/"+modality, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EmbedImage embeds a single image via POST /embed/image (#2444, ADR-0276).
// bytesB64 is the base64-encoded image. Uses the server's configured
// embedder's sidecar (RELATA_ACCEL_ENDPOINT, ADR-177); returns an error (503)
// when the active embedder does not support media. model is an optional
// model hint; pass "" to omit it.
func (v *VectorClient) EmbedImage(ctx context.Context, bytesB64, model string) (*EmbedResponse, error) {
	return v.embedMedia(ctx, "image", bytesB64, model)
}

// EmbedFace embeds a single face crop via POST /embed/face (#2444, ADR-0276).
func (v *VectorClient) EmbedFace(ctx context.Context, bytesB64, model string) (*EmbedResponse, error) {
	return v.embedMedia(ctx, "face", bytesB64, model)
}

// EmbedAudio embeds a single audio clip via POST /embed/audio (#2444, ADR-0276).
func (v *VectorClient) EmbedAudio(ctx context.Context, bytesB64, model string) (*EmbedResponse, error) {
	return v.embedMedia(ctx, "audio", bytesB64, model)
}

// EmbedVideo embeds a single video clip/keyframe via POST /embed/video
// (#2444, ADR-0276).
func (v *VectorClient) EmbedVideo(ctx context.Context, bytesB64, model string) (*EmbedResponse, error) {
	return v.embedMedia(ctx, "video", bytesB64, model)
}
