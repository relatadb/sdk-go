package relata

import (
	"context"
	"net/url"
	"strconv"
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
	if err := s.c.get(ctx, "/v1/jobs", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// JobStatus returns the status of a single background job by name.
func (s *SystemClient) JobStatus(ctx context.Context, name string) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/v1/jobs/"+url.PathEscape(name), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// NotificationRules lists notification rules for the caller's tenant.
func (s *SystemClient) NotificationRules(ctx context.Context) ([]map[string]any, error) {
	var resp []map[string]any
	if err := s.c.get(ctx, "/v1/notification-rules", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// CreateNotificationRule creates a notification rule. A webhook is a delivery
// channel of a rule: set webhook_url (and optionally webhook_headers) on the rule.
func (s *SystemClient) CreateNotificationRule(ctx context.Context, rule map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.postJSON(ctx, "/v1/notification-rules", rule, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// UpdateNotificationRule updates fields of a notification rule (PATCH /v1/notification-rules/:id).
func (s *SystemClient) UpdateNotificationRule(ctx context.Context, id string, patch map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.patchJSON(ctx, "/v1/notification-rules/"+url.PathEscape(id), patch, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SystemConfig returns the runtime feature configuration (GET /v1/system/config).
func (s *SystemClient) SystemConfig(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/v1/system/config", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SystemSpecs returns the capability specification map (GET /v1/system/specs).
func (s *SystemClient) SystemSpecs(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.get(ctx, "/v1/system/specs", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// MCPSessionsOptions filters MCPSessions. Zero values are omitted.
type MCPSessionsOptions struct {
	// SessionID filters to a single session.
	SessionID string
	// Limit caps the number of sessions returned (server default 20, max 100).
	Limit int
}

// MCPSessions lists MCP agent sessions with their tool-call history
// (GET /v1/mcp/sessions). opts may be nil.
func (s *SystemClient) MCPSessions(ctx context.Context, opts *MCPSessionsOptions) (map[string]any, error) {
	params := map[string]string{}
	if opts != nil {
		params["session_id"] = opts.SessionID
		if opts.Limit > 0 {
			params["limit"] = strconv.Itoa(opts.Limit)
		}
	}
	var resp map[string]any
	if err := s.c.get(ctx, encodeGetURL("/v1/mcp/sessions", params), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DeleteNotificationRule deletes a notification rule by ID.
func (s *SystemClient) DeleteNotificationRule(ctx context.Context, id string) (map[string]any, error) {
	var resp map[string]any
	if err := s.c.delete(ctx, "/v1/notification-rules/"+url.PathEscape(id), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
