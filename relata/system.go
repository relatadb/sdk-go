package relata

import (
	"context"
	"encoding/json"
	"net/url"
)

// SystemClient is the synchronous generic system / runtime client for
// background-job status. Model-provider configuration belongs to
// the application layer.
type SystemClient struct {
	c *Client
}

// NewSystemClient constructs a SystemClient that inherits the parent client's
// auth and tenant context. Mirrors the Python reference's SystemClient.from_client.
func NewSystemClient(c *Client) *SystemClient {
	return &SystemClient{c: c}
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
