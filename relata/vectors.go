package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	// EFSearch tunes the HNSW ef-search parameter.
	EFSearch int
}

// HybridSearchOptions configures HybridSearch.
type HybridSearchOptions struct {
	// Purpose is the declared purpose token.
	Purpose string
	// K caps the result count (default 10).
	K int
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
	}
	result, err := v.query(ctx, sql, purpose)
	if err != nil {
		return nil, err
	}
	return result.Rows, nil
}

// HybridSearch performs a hybrid BM25 + vector search via the HYBRID_SEARCH
// operator. At least one of queryText or queryEmbedding (with embeddingSlot)
// must be supplied; when both are supplied the server fuses via reciprocal rank
// fusion (ADR-175).
func (v *VectorClient) HybridSearch(ctx context.Context, objectType string, queryText string, queryEmbedding []float64, embeddingSlot string, opts *HybridSearchOptions) ([]map[string]any, error) {
	if queryText == "" && len(queryEmbedding) == 0 {
		return nil, fmt.Errorf("relata: hybrid_search requires query_text or query_embedding")
	}
	k := 10
	purpose := ""
	if opts != nil {
		if opts.K > 0 {
			k = opts.K
		}
		purpose = opts.Purpose
	}
	args := []string{
		fmt.Sprintf("from => '%s'", objectType),
		fmt.Sprintf("limit => %d", k),
	}
	if queryText != "" {
		args = append(args, fmt.Sprintf("query_text => '%s'", escapeSingleQuotes(queryText)))
	}
	if len(queryEmbedding) > 0 && embeddingSlot != "" {
		embStr, err := json.Marshal(queryEmbedding)
		if err != nil {
			return nil, fmt.Errorf("relata: encode embedding: %w", err)
		}
		args = append(args, fmt.Sprintf("query_embedding => '%s'", escapeSingleQuotes(string(embStr))))
		args = append(args, fmt.Sprintf("embedding_slot => '%s'", embeddingSlot))
	}
	sql := fmt.Sprintf("SELECT * FROM HYBRID_SEARCH(%s)", strings.Join(args, ", "))
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
