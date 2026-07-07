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
