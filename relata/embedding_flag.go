package relata

import "log/slog"

// EmbeddingFlag reports how the query side of a search, RAG or recall result
// was embedded (#5726). Under the server default
// RELATA_QUERY_EMBED_POLICY=lenient a result can be a lexical fallback; the
// SDK logs a warning through log/slog when Semantic is false.
type EmbeddingFlag struct {
	// Mode is "semantic", "lexical-fallback" or "caller-vector".
	Mode string `json:"mode"`
	// Semantic is false when the result is a lexical fallback.
	Semantic bool `json:"semantic"`
	// Model is the query vector's model tag, when one exists.
	Model *string `json:"model"`
	// Reason is "no endpoint", "endpoint error", "model mismatch" or nil.
	Reason *string `json:"reason"`
}

// warnIfLexical logs a warning when f says the result is not semantic.
func warnIfLexical(f *EmbeddingFlag) {
	if f == nil || f.Semantic {
		return
	}
	reason := ""
	if f.Reason != nil {
		reason = *f.Reason
	}
	slog.Warn("relata: result is a lexical fallback, not semantic search; set RELATA_EMBED_URL, "+
		"or RELATA_QUERY_EMBED_POLICY=strict to refuse instead",
		"mode", f.Mode, "reason", reason)
}

// embeddingFlagFromMap reads the flag from a decoded JSON object.
func embeddingFlagFromMap(m map[string]any) *EmbeddingFlag {
	raw, ok := m["embedding"].(map[string]any)
	if !ok {
		return nil
	}
	mode, okMode := raw["mode"].(string)
	semantic, okSem := raw["semantic"].(bool)
	if !okMode || !okSem {
		return nil
	}
	f := &EmbeddingFlag{Mode: mode, Semantic: semantic}
	if s, ok := raw["model"].(string); ok {
		f.Model = &s
	}
	if s, ok := raw["reason"].(string); ok {
		f.Reason = &s
	}
	return f
}
