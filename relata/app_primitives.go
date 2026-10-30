package relata

import (
	"context"
	"net/url"
	"strconv"
)

// AppPrimitivesClient is the sequences + durable job queue client (#5768,
// #5783). A claim returns a fencing token; Renew/Complete/Fail must present it
// and a stale token is rejected with 409.
type AppPrimitivesClient struct {
	c *Client
}

// NewAppPrimitivesClient inherits the parent client's auth and tenant context.
func NewAppPrimitivesClient(c *Client) *AppPrimitivesClient {
	return &AppPrimitivesClient{c: c}
}

// SequenceNext allocates count (default 1 when <= 0) contiguous values.
// idempotencyKey may be empty.
func (a *AppPrimitivesClient) SequenceNext(ctx context.Context, name string, count int64, idempotencyKey string) (map[string]any, error) {
	body := map[string]any{}
	if count > 0 {
		body["count"] = count
	}
	if idempotencyKey != "" {
		body["idempotencyKey"] = idempotencyKey
	}
	var resp map[string]any
	err := a.c.postJSON(ctx, "/sequences/"+pathSegment(name)+"/next", body, &resp)
	return resp, err
}

// SequenceGet returns the value the next allocation would start at.
func (a *AppPrimitivesClient) SequenceGet(ctx context.Context, name string) (map[string]any, error) {
	var resp map[string]any
	err := a.c.get(ctx, "/sequences/"+pathSegment(name), &resp)
	return resp, err
}

// Enqueue adds a job. opts uses the wire keys (priority, runAt, delayMs,
// maxAttempts, backoffMs, idempotencyKey) and may be nil.
func (a *AppPrimitivesClient) Enqueue(ctx context.Context, queue string, payload any, opts map[string]any) (map[string]any, error) {
	body := map[string]any{"payload": payload}
	for k, v := range opts {
		body[k] = v
	}
	var resp map[string]any
	err := a.c.postJSON(ctx, "/queues/"+pathSegment(queue)+"/jobs", body, &resp)
	return resp, err
}

// Claim leases up to max ready jobs (leaseMs / max <= 0 use server defaults).
func (a *AppPrimitivesClient) Claim(ctx context.Context, queue, workerID string, leaseMs int64, max int) ([]any, error) {
	body := map[string]any{"workerId": workerID}
	if leaseMs > 0 {
		body["leaseMs"] = leaseMs
	}
	if max > 0 {
		body["max"] = max
	}
	var resp map[string]any
	if err := a.c.postJSON(ctx, "/queues/"+pathSegment(queue)+"/claim", body, &resp); err != nil {
		return nil, err
	}
	jobs, _ := resp["jobs"].([]any)
	return jobs, nil
}

// Renew extends a held lease.
func (a *AppPrimitivesClient) Renew(ctx context.Context, jobID string, fencingToken, leaseMs int64) (map[string]any, error) {
	body := map[string]any{"fencingToken": fencingToken}
	if leaseMs > 0 {
		body["leaseMs"] = leaseMs
	}
	var resp map[string]any
	err := a.c.postJSON(ctx, "/jobs/"+pathSegment(jobID)+"/renew", body, &resp)
	return resp, err
}

// Complete marks a held job done; result may be nil.
func (a *AppPrimitivesClient) Complete(ctx context.Context, jobID string, fencingToken int64, result any) (map[string]any, error) {
	body := map[string]any{"fencingToken": fencingToken}
	if result != nil {
		body["result"] = result
	}
	var resp map[string]any
	err := a.c.postJSON(ctx, "/jobs/"+pathSegment(jobID)+"/complete", body, &resp)
	return resp, err
}

// Fail reports a failure: retried with backoff, then dead-lettered. opts uses
// the wire keys (error, retryAfterMs, retryable) and may be nil.
func (a *AppPrimitivesClient) Fail(ctx context.Context, jobID string, fencingToken int64, opts map[string]any) (map[string]any, error) {
	body := map[string]any{"fencingToken": fencingToken}
	for k, v := range opts {
		body[k] = v
	}
	var resp map[string]any
	err := a.c.postJSON(ctx, "/jobs/"+pathSegment(jobID)+"/fail", body, &resp)
	return resp, err
}

// ListJobs inspects a queue; state and limit are optional ("" / 0 to omit).
func (a *AppPrimitivesClient) ListJobs(ctx context.Context, queue, state string, limit int) (map[string]any, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/queues/" + pathSegment(queue) + "/jobs"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var resp map[string]any
	err := a.c.get(ctx, path, &resp)
	return resp, err
}

// GetJob fetches one job.
func (a *AppPrimitivesClient) GetJob(ctx context.Context, queue, jobID string) (map[string]any, error) {
	var resp map[string]any
	err := a.c.get(ctx, "/queues/"+pathSegment(queue)+"/jobs/"+pathSegment(jobID), &resp)
	return resp, err
}
