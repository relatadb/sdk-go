package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// WorkflowStepStatus is the per-step status within a WorkflowRun.
type WorkflowStepStatus struct {
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	AttemptCount int     `json:"attempt_count"`
	LastError    *string `json:"last_error"`
}

// WorkflowRun is the typed response for WorkflowRun — surfaces durable-resume
// fields written by #711/#713 (retry backoff + detection-failure persistence).
type WorkflowRun struct {
	RunID        string               `json:"run_id"`
	WorkflowName string               `json:"workflow_name"`
	Status       string               `json:"status"`
	AttemptCount int                  `json:"attempt_count"`
	LastError    *string              `json:"last_error"`
	Steps        []WorkflowStepStatus `json:"steps"`
	CreatedAt    string               `json:"created_at"`
	UpdatedAt    string               `json:"updated_at"`
}

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

// JobStatus returns the status of a single background job by name.
func (s *SystemClient) JobStatus(ctx context.Context, name string) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/jobs/"+url.PathEscape(name), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListWorkflows returns registered workflow definition names.
func (s *SystemClient) ListWorkflows(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/workflows", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetWorkflow fetches one workflow definition by name.
func (s *SystemClient) GetWorkflow(ctx context.Context, name string) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/workflows/"+url.PathEscape(name), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RegisterWorkflow registers a workflow definition. steps is a list of step maps,
// e.g. {"name": "..", "kind": "query", "sql": "..", "depends_on": [0]}.
func (s *SystemClient) RegisterWorkflow(ctx context.Context, name string, steps []map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.postJSON(ctx, "/workflows", map[string]any{"name": name, "steps": steps}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RunWorkflow starts a workflow execution and returns run_id + runnable steps.
func (s *SystemClient) RunWorkflow(ctx context.Context, name string) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.postJSON(ctx, fmt.Sprintf("/workflows/%s/run", url.PathEscape(name)), map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// WorkflowStatus returns the latest run status for a workflow by name.
func (s *SystemClient) WorkflowStatus(ctx context.Context, name string) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, fmt.Sprintf("/workflows/%s/status", url.PathEscape(name)), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// WorkflowRun returns step-level detail of a run by run_id — surfaces
// attempt_count, last_error, per-step status (fields written by #711/#713).
func (s *SystemClient) WorkflowRun(ctx context.Context, runID string) (WorkflowRun, error) {
	var resp WorkflowRun
	if err := s.c.get(ctx, "/workflows/runs/"+url.PathEscape(runID), &resp); err != nil {
		return WorkflowRun{}, err
	}
	return resp, nil
}

// FeedHealth returns the RIFN feed broker health status.
func (s *SystemClient) FeedHealth(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/feed/health", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// FeedChannels lists available feed channels.
func (s *SystemClient) FeedChannels(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/feed/channels", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// FeedPublish publishes a record to a feed channel.
func (s *SystemClient) FeedPublish(ctx context.Context, channel string, payload json.RawMessage) (map[string]any, error) {
	body := map[string]any{"channel": channel, "payload": payload}
	var resp map[string]any
	if err := s.c.postJSON(ctx, "/feed/publish", body, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// NotificationRules lists notification rules for the caller's tenant.
func (s *SystemClient) NotificationRules(ctx context.Context) ([]map[string]any, error) {
	var resp []map[string]any
	if err := s.c.get(ctx, "/notifications/rules", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// CreateNotificationRule creates a notification rule.
func (s *SystemClient) CreateNotificationRule(ctx context.Context, rule map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.postJSON(ctx, "/notifications/rules", rule, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DeleteNotificationRule deletes a notification rule by ID.
func (s *SystemClient) DeleteNotificationRule(ctx context.Context, id string) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.delete(ctx, "/notifications/rules/"+url.PathEscape(id), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListPipelines lists registered ingest pipelines.
func (s *SystemClient) ListPipelines(ctx context.Context) ([]map[string]any, error) {
	var resp []map[string]any
	if err := s.c.get(ctx, "/pipelines", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DefinePipeline defines a new ingest pipeline.
func (s *SystemClient) DefinePipeline(ctx context.Context, definition map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.postJSON(ctx, "/pipelines", definition, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
