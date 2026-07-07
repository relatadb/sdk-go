package relata

import "context"

// SystemClient is the synchronous system / runtime client — LLM configuration,
// LLM connectivity test, and background-jobs status. Pairs the agent surfaces
// (McpClient / A2AClient) with the operator-side runtime config.
type SystemClient struct {
	c *Client
}

// NewSystemClient constructs a SystemClient that inherits the parent client's
// auth and tenant context. Mirrors the Python reference's SystemClient.from_client.
func NewSystemClient(c *Client) *SystemClient {
	return &SystemClient{c: c}
}

// TestLLMOptions configures SystemClient.TestLLM.
type TestLLMOptions struct {
	// Model overrides the configured default model.
	Model string
}

// LLMConfig returns the configured LLM endpoint + model roster.
func (s *SystemClient) LLMConfig(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/config/llm", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// TestLLM sends a test prompt to the configured LLM endpoint (or a specific
// model) and returns the round-trip result + latency. Used by operators to
// verify connectivity before pointing agents at the server.
func (s *SystemClient) TestLLM(ctx context.Context, prompt string, opts *TestLLMOptions) (map[string]any, error) {
	payload := map[string]any{"prompt": prompt}
	if opts != nil && opts.Model != "" {
		payload["model"] = opts.Model
	}
	var resp map[string]any
	if err := s.c.postJSON(ctx, "/config/llm/test", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// JobsStatus returns the status of every background job (continuous-pattern
// detectors, MV refresh, embedder worker, orphan-blob sweep, …).
func (s *SystemClient) JobsStatus(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/jobs", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
