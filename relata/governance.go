package relata

import (
	"context"
	"fmt"
	"net/url"
)

// GovernanceClient is the synchronous governance surface — rules, retention
// (legal holds + WORM), breakglass, alerts, and DSAR. Every call inherits the
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
	// ObjectID limits the hold to a specific row; omit for a type-wide hold.
	ObjectID string
	// Reason is an optional human-friendly reason.
	Reason string
}

// RequestBreakglassOptions configures the optional fields of RequestBreakglass.
type RequestBreakglassOptions struct {
	// Purpose is the declared query purpose. The server defaults to
	// "humint_unmask" when omitted.
	Purpose string
	// Justification is a free-text justification for the emergency access,
	// captured for audit.
	Justification string
}

// ApproveBreakglassOptions configures the optional fields of ApproveBreakglass.
type ApproveBreakglassOptions struct {
	// Note is an optional second-officer approver note.
	Note string
}

// ListAlertsOptions configures the optional filters of ListAlerts.
type ListAlertsOptions struct {
	// Severity filters to one severity level.
	Severity string
	// SinceNS filters to alerts at/after the given nanosecond cursor.
	SinceNS int64
	// Limit caps the response. Defaults to 100.
	Limit int
}

// UpdateAlertOptions configures the optional fields of UpdateAlert.
type UpdateAlertOptions struct {
	// Status transitions the alert (e.g. "ack", "closed").
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
	path := "/rules"
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

// CreateRule creates a detection rule. The map shape matches the server's
// RuleSpec (name, object_type, condition, action, …). Returns the created rule
// record including its server-assigned id.
//
// purpose is mandatory server-side (POST /rules?purpose=<p>) — pass it via
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
	path := "/rules?purpose=" + url.QueryEscape(purpose)
	if err := g.c.postJSON(ctx, path, rule, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// DisableRule disables (logically deletes) a rule by id.
func (g *GovernanceClient) DisableRule(ctx context.Context, ruleID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.delete(ctx, fmt.Sprintf("/rules/%s", ruleID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ImportSigmaOptions configures the optional fields of ImportSigma.
type ImportSigmaOptions struct {
	// Purpose overrides the client's DefaultPurpose for this call.
	// The server rejects the request with 403 if neither is set.
	Purpose string
}

// ImportSigma imports a Sigma rule (YAML string). Returns the import summary
// (rules_imported, rules_skipped, errors).
//
// The server (POST /rules/sigma?purpose=<p>) requires purpose as a query
// param and the raw YAML text as the request body — not a JSON envelope.
func (g *GovernanceClient) ImportSigma(ctx context.Context, sigmaYAML string, opts *ImportSigmaOptions) (map[string]any, error) {
	override := ""
	if opts != nil {
		override = opts.Purpose
	}
	purpose, err := g.resolvePurpose(override)
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	path := "/rules/sigma?purpose=" + url.QueryEscape(purpose)
	if err := g.c.postRaw(ctx, path, "application/x-yaml", []byte(sigmaYAML), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SnoozeRule temporarily disables a rule for durationSecs (#967).
func (g *GovernanceClient) SnoozeRule(ctx context.Context, ruleID string, durationSecs int) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/rules/"+ruleID+"/snooze", map[string]any{"duration_secs": durationSecs}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SuppressRuleOptions configures the optional fields of SuppressRule.
type SuppressRuleOptions struct {
	// Condition is an informational note about why entityID is suppressed.
	Condition string
}

// SuppressRule permanently suppresses future matches of the rule for a
// specific entity (#967). The server requires entity_id — a bare match
// pattern is not accepted.
func (g *GovernanceClient) SuppressRule(ctx context.Context, ruleID, entityID string, opts *SuppressRuleOptions) (map[string]any, error) {
	body := map[string]any{"entity_id": entityID}
	if opts != nil && opts.Condition != "" {
		body["condition"] = opts.Condition
	}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/rules/"+ruleID+"/suppress", body, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// AddRuleException adds an exception so specific matches are ignored (#967).
func (g *GovernanceClient) AddRuleException(ctx context.Context, ruleID string, exception map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/rules/"+ruleID+"/exceptions", exception, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetRuleTuning retrieves the tuning state (snoozes, suppressions, exceptions) (#967).
func (g *GovernanceClient) GetRuleTuning(ctx context.Context, ruleID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/rules/"+ruleID+"/tuning", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListRetentionPolicies lists configured retention policies.
func (g *GovernanceClient) ListRetentionPolicies(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/retention/policies", &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "policies"), nil
}

// ListLegalHolds lists active legal holds.
func (g *GovernanceClient) ListLegalHolds(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/retention/holds", &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "holds"), nil
}

// PlaceLegalHold places a legal hold on an object type (optionally on a
// specific row).
func (g *GovernanceClient) PlaceLegalHold(ctx context.Context, caseID, objectType string, opts *PlaceLegalHoldOptions) (map[string]any, error) {
	payload := map[string]any{
		"case_id":     caseID,
		"object_type": objectType,
	}
	if opts != nil {
		if opts.ObjectID != "" {
			payload["object_id"] = opts.ObjectID
		}
		if opts.Reason != "" {
			payload["reason"] = opts.Reason
		}
	}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/retention/holds", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// LiftLegalHold lifts (removes) a legal hold by case id.
func (g *GovernanceClient) LiftLegalHold(ctx context.Context, caseID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.delete(ctx, fmt.Sprintf("/retention/holds/%s", caseID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListWormPolicies lists WORM (write-once-read-many) retention policies.
func (g *GovernanceClient) ListWormPolicies(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, "/retention/worm", &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "policies"), nil
}

// SetWormPolicy sets WORM retention for an object type. Rows cannot be mutated
// or purged until retentionSecs elapses from their system_from.
func (g *GovernanceClient) SetWormPolicy(ctx context.Context, objectType string, retentionSecs int) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.postJSON(ctx, fmt.Sprintf("/retention/worm/%s", objectType), map[string]any{"retention_secs": retentionSecs}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RequestBreakglass requests emergency HUMINT breakglass access (fixed 4h
// window). sourceID is the masked source ID being unmasked (e.g.
// "SRC-0042"). Approval requires two distinct officers (ApproveBreakglass).
func (g *GovernanceClient) RequestBreakglass(ctx context.Context, sourceID string, opts *RequestBreakglassOptions) (map[string]any, error) {
	payload := map[string]any{"source_id": sourceID}
	if opts != nil {
		if opts.Purpose != "" {
			payload["purpose"] = opts.Purpose
		}
		if opts.Justification != "" {
			payload["justification"] = opts.Justification
		}
	}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/humint/breakglass/request", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ApproveBreakglass records a second-officer approval for a breakglass request.
// The server enforces that the requester cannot approve their own request and
// that two distinct approvals are required.
func (g *GovernanceClient) ApproveBreakglass(ctx context.Context, requestID string, opts *ApproveBreakglassOptions) (map[string]any, error) {
	payload := map[string]any{"request_id": requestID}
	if opts != nil && opts.Note != "" {
		payload["note"] = opts.Note
	}
	var resp map[string]any
	if err := g.c.postJSON(ctx, "/humint/breakglass/approve", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// BreakglassStatus looks up the status of a breakglass request.
func (g *GovernanceClient) BreakglassStatus(ctx context.Context, requestID string) (map[string]any, error) {
	var resp map[string]any
	if err := g.c.get(ctx, fmt.Sprintf("/humint/breakglass/status/%s", requestID), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListAlerts lists alerts, optionally filtered by severity / since-cursor.
func (g *GovernanceClient) ListAlerts(ctx context.Context, opts *ListAlertsOptions) ([]map[string]any, error) {
	limit := 100
	params := map[string]string{"limit": fmt.Sprintf("%d", limit)}
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
	}
	var resp map[string]any
	if err := g.c.get(ctx, encodeGetURL("/alerts/list", params), &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "alerts"), nil
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
	if err := g.c.patchJSON(ctx, fmt.Sprintf("/alerts/update/%s", alertID), payload, &resp); err != nil {
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
	if err := g.c.get(ctx, encodeGetURL("/gdpr/dsar", stringParams), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
