package relata

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// MediaWorkClaimHeader is the header every bulk write-back for an
// openetl/media/<work_id> source must carry (#5938).
const MediaWorkClaimHeader = "X-Relata-Claim"

// MediaWorkClaimHeaders returns the X-Relata-Claim header for a raw
// POST /ingest/bulk write-back. BulkOptions.MediaWorkClaim sets it for you.
func MediaWorkClaimHeaders(claimID uint64) map[string]string {
	return map[string]string{MediaWorkClaimHeader: strconv.FormatUint(claimID, 10)}
}

// MediaWork item states accepted by MediaWorkClient.List.
const (
	MediaWorkQueued     = "queued"
	MediaWorkClaimed    = "claimed"
	MediaWorkWriting    = "writing"
	MediaWorkCompleted  = "completed"
	MediaWorkDeadLetter = "dead_letter"
	MediaWorkCancelled  = "cancelled"
)

// MediaWork failure classes for MediaWorkClient.Fail.
const (
	MediaWorkRetryable = "retryable"
	MediaWorkPermanent = "permanent"
)

// MediaWorkClient wraps the /media/work routes used by OpenETL: lease items
// with Claim, keep the lease with Heartbeat, write rows through
// IngestClient.Bulk with BulkOptions.MediaWorkClaim, then Complete or Fail.
// Complete succeeds only when the batch and child counts equal what Relata
// durably holds.
type MediaWorkClient struct {
	c *Client
}

// NewMediaWorkClient inherits the parent client's auth and tenant context.
func NewMediaWorkClient(c *Client) *MediaWorkClient {
	return &MediaWorkClient{c: c}
}

func mediaWorkPath(workID, suffix string) string {
	return "/media/work/" + url.PathEscape(workID) + suffix
}

// MediaWorkClaimOptions tunes Claim. Zero values use the server defaults
// (1 item, 120000 ms lease).
type MediaWorkClaimOptions struct {
	MaxItems int
	LeaseMs  int
}

// Qualification calls GET /media/qualification: the supported media tasks and
// the strongest claim the recorded evidence supports for each (stored_only,
// extracted_features or evaluated_retrieval).
func (m *MediaWorkClient) Qualification(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := m.c.get(ctx, "/media/qualification", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// List calls GET /media/work. state "" returns every state; limit <= 0 uses
// the server default.
func (m *MediaWorkClient) List(ctx context.Context, state string, limit int) (map[string]any, error) {
	q := url.Values{}
	if state != "" {
		switch state {
		case MediaWorkQueued, MediaWorkClaimed, MediaWorkWriting, MediaWorkCompleted,
			MediaWorkDeadLetter, MediaWorkCancelled:
		default:
			return nil, fmt.Errorf("relata: invalid media work state %q", state)
		}
		q.Set("state", state)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/media/work"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out map[string]any
	if err := m.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Get calls GET /media/work/{id}: durable status; 404 when unknown.
func (m *MediaWorkClient) Get(ctx context.Context, workID string) (map[string]any, error) {
	var out map[string]any
	if err := m.c.get(ctx, mediaWorkPath(workID, ""), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Claim calls POST /media/work/claim. The result is {"items": [...]}; an empty
// list means nothing is deliverable now (no long poll).
func (m *MediaWorkClient) Claim(ctx context.Context, claimant string, opts *MediaWorkClaimOptions) (map[string]any, error) {
	body := map[string]any{"claimant": claimant}
	if opts != nil {
		if opts.MaxItems > 0 {
			body["max_items"] = opts.MaxItems
		}
		if opts.LeaseMs > 0 {
			body["lease_ms"] = opts.LeaseMs
		}
	}
	var out map[string]any
	if err := m.c.postJSON(ctx, "/media/work/claim", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Heartbeat calls POST /media/work/{id}/heartbeat (409 once the lease lapsed).
// leaseMs <= 0 uses the server default.
func (m *MediaWorkClient) Heartbeat(ctx context.Context, workID string, claimID uint64, leaseMs int) (map[string]any, error) {
	body := map[string]any{"claim_id": claimID}
	if leaseMs > 0 {
		body["lease_ms"] = leaseMs
	}
	var out map[string]any
	if err := m.c.postJSON(ctx, mediaWorkPath(workID, "/heartbeat"), body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Complete calls POST /media/work/{id}/complete: 409 when batches/children
// differ from what Relata durably holds.
func (m *MediaWorkClient) Complete(ctx context.Context, workID string, claimID uint64, producerVersion string, batches, children int) (map[string]any, error) {
	body := map[string]any{
		"claim_id":         claimID,
		"producer_version": producerVersion,
		"batches":          batches,
		"children":         children,
	}
	var out map[string]any
	if err := m.c.postJSON(ctx, mediaWorkPath(workID, "/complete"), body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Fail calls POST /media/work/{id}/fail. class is MediaWorkRetryable
// (redelivered with back-off) or MediaWorkPermanent (dead-lettered). code and
// detail are optional.
func (m *MediaWorkClient) Fail(ctx context.Context, workID string, claimID uint64, class, code, detail string) (map[string]any, error) {
	if class != MediaWorkRetryable && class != MediaWorkPermanent {
		return nil, fmt.Errorf("relata: media work failure class must be %q or %q", MediaWorkRetryable, MediaWorkPermanent)
	}
	body := map[string]any{"claim_id": claimID, "class": class}
	if code != "" {
		body["code"] = code
	}
	if detail != "" {
		body["detail"] = detail
	}
	var out map[string]any
	if err := m.c.postJSON(ctx, mediaWorkPath(workID, "/fail"), body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
