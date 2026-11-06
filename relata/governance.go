package relata

import (
	"context"
	"fmt"
	"net/url"
)

// GovernanceClient is the synchronous governance surface — rules, retention
// (legal holds + WORM), alerts, and DSAR. Every call inherits the
// parent client's purpose/tenant/auth context. Use NewGovernanceClient to
// construct one from a *Client.
type GovernanceClient struct {
	c *Client
}

// NewGovernanceClient constructs a GovernanceClient that inherits the parent
// client's auth, tenant, and purpose context. Mirrors the Python reference's
// GovernanceClient.from_client classmethod.
func NewGovernanceClient(c *Client) *GovernanceClient {
	return &GovernanceClient{c: c}
}

// PlaceLegalHoldOptions configures the optional fields of PlaceLegalHold.
type PlaceLegalHoldOptions struct {
	// Field scopes the hold to rows where Field equals Value; set both
	// together, or leave both empty for a type-wide hold. The server's
	// POST /v1/retention/holds only reads case_id/object_type/field/value —
	// any other key is silently ignored (#2465), so Field/Value is the
	// only supported row-scoping mechanism.
	Field string
	// Value is the column value to match when Field is set.
	Value string
}

// ListAlertsOptions configures the optional filters of ListAlerts.
type ListAlertsOptions struct {
	// Severity filters to one severity level.
	Severity string
	// SinceNS filters to alerts at/after the given nanosecond cursor.
	SinceNS int64
	// Limit caps the response page (server maximum 1000). Defaults to 100.
	Limit int
	// Cursor continues from a previous page's NextCursor.
	Cursor string
	// NewestFirst lists the newest alerts first. The server default is
	// oldest-first, so a capped page would otherwise hide the newest alerts.
	NewestFirst bool
}

// AlertsPage is one page of ListAlertsPage.
type AlertsPage struct {
	// Items are the alerts on this page.
	Items []map[string]any
	// Total counts all matching alerts before pagination.
	Total int
	// NextCursor is empty on the last page; pass it as ListAlertsOptions.Cursor
	// to continue in the same order.
	NextCursor string
}

// UpdateAlertOptions configures the optional fields of UpdateAlert.
type UpdateAlertOptions struct {
	// Status transitions the alert: "open", "acknowledged", "resolved" or
	// "false_positive" (the server rejects any other value with 400).
	Status string
	// Assignee sets the owner.
	Assignee string
	// Note appends an investigative note.
	Note string
}

// SubmitDSAROptions configures the optional scope of SubmitDSAR.
type SubmitDSAROptions struct {
	// Scope filters the DSAR to a domain ("email", "financial", …).
	Scope string
}

// ListRules lists detection rules. A non-empty objectType filters to one type.
func (g *GovernanceClient) ListRules(ctx context.Context, objectType string) ([]map[string]any, error) {
	path := "/v1/rules"
	if objectType != "" {
		path += "?object_type=" + objectType
	}
	var resp map[string]any
	if err := g.c.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "rules"), nil
}

// CreateRuleOptions configures the optional fields of CreateRule.
type CreateRuleOptions struct {
	// Purpose overrides the client's DefaultPurpose for this call.
	// The server rejects the request with 403 if neither is set.
	Purpose string
}

// resolvePurpose returns the effective purpose (override, else the client's
// DefaultPurpose), or ErrPurposeRequired if neither is set.
func (g *GovernanceClient) resolvePurpose(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if g.c.defaultPurpose == "" {
		return "", ErrPurposeRequired
	}
	return g.c.defaultPurpose, nil
}

// CreateRule creates a prepared detection rule. The server requires name,
// condition_sql, and target_type; severity, mitre_technique, trigger, and
// action are optional. Returns the created rule
// record including its server-assigned id.
//
// purpose is mandatory server-side (POST /v1/rules?purpose=<p>) — pass it via
// opts.Purpose or set DefaultPurpose on the parent client.
func (g *GovernanceClient) CreateRule(ctx context.Context, rule map[string]any, opts *CreateRuleOptions) (map[string]any, error) {
	override := ""
	if opts != nil {
		override = opts.Purpose
	}
	purpose, err := g.resolvePurpose(override)
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	path := "/v1/rules?purpose=" + url.QueryEscape(purpose)
	if err := g.c.postJSON(ctx, path, rule, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DisableRule disables (logically deletes) a rule by id.
func (g *GovernanceClient) DisableRule(ctx context.Context, ruleID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.delete(ctx, "/v1/rules/"+pathSegment(ruleID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SnoozeRule temporarily disables a rule for durationSecs (#967).
func (g *GovernanceClient) SnoozeRule(ctx context.Context, ruleID string, durationSecs int) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/v1/rules/"+pathSegment(ruleID)+"/snooze", map[string]any{"duration_secs": durationSecs}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SuppressRuleOptions configures the optional fields of SuppressRule.
type SuppressRuleOptions struct {
	// Condition is an informational note about why entityID is suppressed.
	Condition string
	// DurationSecs is how long the suppression lasts. The server refuses a
	// suppression without a positive duration (#3197: suppressions expire).
	DurationSecs int
}

// SuppressRule permanently suppresses future matches of the rule for a
// specific entity (#967). The server requires entity_id — a bare match
// pattern is not accepted.
func (g *GovernanceClient) SuppressRule(ctx context.Context, ruleID, entityID string, opts *SuppressRuleOptions) (map[string]any, error) {
	body := map[string]any{"entity_id": entityID}
	if opts != nil && opts.Condition != "" {
		body["condition"] = opts.Condition
	}
	if opts != nil && opts.DurationSecs > 0 {
		body["duration_secs"] = opts.DurationSecs
	}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/v1/rules/"+pathSegment(ruleID)+"/suppress", body, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// AddRuleException adds an exception so specific matches are ignored (#967).
func (g *GovernanceClient) AddRuleException(ctx context.Context, ruleID string, exception map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/v1/rules/"+pathSegment(ruleID)+"/exceptions", exception, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetRuleTuning retrieves the tuning state (snoozes, suppressions, exceptions) (#967).
func (g *GovernanceClient) GetRuleTuning(ctx context.Context, ruleID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/v1/rules/"+pathSegment(ruleID)+"/tuning", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetRule fetches one detection rule by ID (GET /v1/rules/:id).
func (g *GovernanceClient) GetRule(ctx context.Context, ruleID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/v1/rules/"+pathSegment(ruleID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RemoveRuleException removes a tuning exception (DELETE /v1/rules/:id/exceptions/:eid).
func (g *GovernanceClient) RemoveRuleException(ctx context.Context, ruleID, exceptionID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.delete(ctx, "/v1/rules/"+pathSegment(ruleID)+"/exceptions/"+pathSegment(exceptionID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SetRetentionPolicy sets a retention policy for a type (POST /v1/retention/policies).
// action is "delete" or "tier:cold"; an empty action defaults to "delete".
func (g *GovernanceClient) SetRetentionPolicy(ctx context.Context, objectType string, maxAgeDays int, action string) (map[string]any, error) {
	if action == "" {
		action = "delete"
	}
	body := map[string]any{"object_type": objectType, "max_age_days": maxAgeDays, "action": action}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/v1/retention/policies", body, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListRetentionPolicies lists configured retention policies.
func (g *GovernanceClient) ListRetentionPolicies(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/v1/retention/policies", &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "policies"), nil
}

// ListLegalHolds lists active legal holds.
func (g *GovernanceClient) ListLegalHolds(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/v1/retention/holds", &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "holds"), nil
}

// PlaceLegalHold places a legal hold on an object type (optionally scoped to
// rows where opts.Field equals opts.Value).
func (g *GovernanceClient) PlaceLegalHold(ctx context.Context, caseID, objectType string, opts *PlaceLegalHoldOptions) (map[string]any, error) {
	payload := map[string]any{
		"case_id":     caseID,
		"object_type": objectType,
	}
	if opts != nil {
		if opts.Field != "" {
			payload["field"] = opts.Field
		}
		if opts.Value != "" {
			payload["value"] = opts.Value
		}
	}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/v1/retention/holds", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// LiftLegalHold lifts (removes) a legal hold by case id.
func (g *GovernanceClient) LiftLegalHold(ctx context.Context, caseID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.delete(ctx, "/v1/retention/holds/"+pathSegment(caseID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListWormPolicies lists the object types under WORM (write-once-read-many),
// one entry per type sorted by object_type:
// {"object_type", "worm": true, "retention": "indefinite", "retention_secs": nil}
// (#6045). A server that only sends "worm_types" (type names) is still read;
// each name becomes {"object_type": name, "worm": true}.
func (g *GovernanceClient) ListWormPolicies(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/v1/retention/worm", &resp); err != nil {
		return nil, err
	}
	if _, ok := resp["policies"].([]any); ok {
		return unwrapList(resp, "policies"), nil
	}
	out := []map[string]any{}
	if types, ok := resp["worm_types"].([]any); ok {
		for _, t := range types {
			if name, ok := t.(string); ok {
				out = append(out, map[string]any{"object_type": name, "worm": true})
			}
		}
	}
	return out, nil
}

// SetWormPolicy puts objectType under WORM. The lock is indefinite: rows of the
// type cannot be mutated or purged, and the server stores no finite retention
// ("retention_secs" in the reply is always nil) (#6045).
//
// retentionSecs is optional and only recorded as a request: a fresh type echoes
// it as "requested_retention_secs". A finite value on a type already under WORM
// is refused with 409 worm-retention-cannot-shorten (ErrConflict); call with no
// retentionSecs to re-assert the lock. Only the first value is used.
func (g *GovernanceClient) SetWormPolicy(ctx context.Context, objectType string, retentionSecs ...int) (map[string]any, error) {
	body := map[string]any{}
	if len(retentionSecs) > 0 {
		body["retention_secs"] = retentionSecs[0]
	}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/v1/retention/worm/"+pathSegment(objectType), body, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListAlerts lists one page of alerts, optionally filtered by severity /
// since-cursor. Use ListAlertsPage to also receive the pagination cursor.
func (g *GovernanceClient) ListAlerts(ctx context.Context, opts *ListAlertsOptions) ([]map[string]any, error) {
	page, err := g.ListAlertsPage(ctx, opts)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// ListAlertsPage lists one page of alerts with its total and NextCursor.
func (g *GovernanceClient) ListAlertsPage(ctx context.Context, opts *ListAlertsOptions) (*AlertsPage, error) {
	params := map[string]string{"limit": "100"}
	if opts != nil {
		if opts.Limit > 0 {
			params["limit"] = fmt.Sprintf("%d", opts.Limit)
		}
		if opts.Severity != "" {
			params["severity"] = opts.Severity
		}
		if opts.SinceNS > 0 {
			params["since_ns"] = fmt.Sprintf("%d", opts.SinceNS)
		}
		if opts.Cursor != "" {
			params["cursor"] = opts.Cursor
		}
		if opts.NewestFirst {
			params["order"] = "desc"
		}
	}
	var resp map[string]any
	if err := g.c.get(ctx, encodeGetURL("/v1/alerts", params), &resp); err != nil {
		return nil, err
	}
	items := unwrapList(resp, "items")
	if items == nil {
		items = unwrapList(resp, "alerts")
	}
	page := &AlertsPage{Items: items, Total: len(items)}
	if total, ok := resp["total"].(float64); ok {
		page.Total = int(total)
	}
	if next, ok := resp["nextCursor"].(string); ok {
		page.NextCursor = next
	}
	return page, nil
}

// UpdateAlert updates an alert (ack, assign, close, add a note).
func (g *GovernanceClient) UpdateAlert(ctx context.Context, alertID string, opts *UpdateAlertOptions) (map[string]any, error) {
	payload := map[string]any{}
	if opts != nil {
		if opts.Status != "" {
			payload["status"] = opts.Status
		}
		if opts.Assignee != "" {
			payload["assignee"] = opts.Assignee
		}
		if opts.Note != "" {
			payload["note"] = opts.Note
		}
	}
	var resp map[string]any
	if err := g.c.patchJSON(ctx, "/v1/alerts/"+pathSegment(alertID), payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SubmitDSAR files a GDPR Data Subject Access Request.
func (g *GovernanceClient) SubmitDSAR(ctx context.Context, subjectIdentity, reason string, opts *SubmitDSAROptions) (map[string]any, error) {
	params := map[string]any{
		"subject_identity": subjectIdentity,
		"reason":           reason,
	}
	if opts != nil && opts.Scope != "" {
		params["scope"] = opts.Scope
	}
	// Python sends DSAR via GET with query params.
	stringParams := make(map[string]string, len(params))
	for k, v := range params {
		stringParams[k] = fmt.Sprintf("%v", v)
	}
	var resp map[string]any
	if err := g.c.get(ctx, encodeGetURL("/v1/gdpr/dsar", stringParams), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// EraseOptions configures GovernanceClient.Erase.
type EraseOptions struct {
	// Reason is recorded on the erasure receipt.
	Reason string
	// TenantID names the tenant to erase from (unscoped admin callers only; an
	// attributed caller's own tenant always wins).
	TenantID string
	// Physical requests physical erasure in addition to the logical tombstone.
	Physical bool
}

// Erase files a GDPR erasure for subjectID (POST /v1/gdpr/erase) and returns
// the decoded receipt / operation response.
func (g *GovernanceClient) Erase(ctx context.Context, subjectID string, opts *EraseOptions) (map[string]any, error) {
	payload := map[string]any{"subject_id": subjectID}
	if opts != nil {
		if opts.Reason != "" {
			payload["reason"] = opts.Reason
		}
		if opts.TenantID != "" {
			payload["tenant_id"] = opts.TenantID
		}
		if opts.Physical {
			payload["physical"] = true
		}
	}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/v1/gdpr/erase", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DeletionStatus returns the progress of a deletion operation
// (GET /v1/gdpr/deletions/{operation_id}).
func (g *GovernanceClient) DeletionStatus(ctx context.Context, operationID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/v1/gdpr/deletions/"+pathSegment(operationID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// VerifyReceipt verifies a signed erasure receipt (POST /v1/gdpr/verify-receipt).
// receipt carries receipt_id, subject, erased_at, rows_shredded and
// receipt_signature exactly as returned by Erase.
func (g *GovernanceClient) VerifyReceipt(ctx context.Context, receipt map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/v1/gdpr/verify-receipt", receipt, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
