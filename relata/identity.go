package relata

import (
	"context"
	"fmt"
	"regexp"
)

// IdentityClient is the synchronous identity + lookup client. It wraps identity
// labels, uncertainty reporting, lookup tables (Splunk `lookup` parity), and
// the ERASE SUBJECT governed-tombstone path.
type IdentityClient struct {
	c *Client
}

// NewIdentityClient constructs an IdentityClient that inherits the parent
// client's auth and tenant context. Mirrors the Python reference's
// IdentityClient.from_client.
func NewIdentityClient(c *Client) *IdentityClient {
	return &IdentityClient{c: c}
}

// LabelOptions configures IdentityClient.Label.
type LabelOptions struct {
	// Confidence clamps to [0, 1]; defaults to 1.0.
	Confidence float64
	// SourceID attaches a provenance source.
	SourceID string
}

// RecordUncertaintyOptions configures IdentityClient.RecordUncertainty.
type RecordUncertaintyOptions struct {
	// SuggestedAlternatives lists alternate identity candidates for human review.
	SuggestedAlternatives []string
}

// RegisterLookupOptions configures IdentityClient.RegisterLookup.
type RegisterLookupOptions struct {
	// ValueColumns restricts which CSV columns are indexed for retrieval.
	ValueColumns []string
}

// EraseSubjectOptions configures IdentityClient.EraseSubject.
type EraseSubjectOptions struct {
	// Certify adds the CERTIFY keyword for Art. 17 attestation. Defaults to true.
	Certify bool
	// CertifySet reports whether Certify was explicitly set.
	CertifySet bool
}

// Label assigns a canonical label to an identity.
func (i *IdentityClient) Label(ctx context.Context, identity, label string, opts *LabelOptions) (map[string]any, error) {
	confidence := 1.0
	payload := map[string]any{
		"identity":   identity,
		"label":      label,
		"confidence": confidence,
	}
	if opts != nil {
		if opts.Confidence != 0 {
			payload["confidence"] = opts.Confidence
		}
		if opts.SourceID != "" {
			payload["source_id"] = opts.SourceID
		}
	}
	var resp map[string]any
	if err := i.c.postJSON(ctx, "/identity/label", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RecordUncertainty records uncertainty about an identity match. The server
// surfaces these for human review.
func (i *IdentityClient) RecordUncertainty(ctx context.Context, identity, reason string, opts *RecordUncertaintyOptions) (map[string]any, error) {
	params := map[string]string{
		"identity": identity,
		"reason":   reason,
	}
	if opts != nil && len(opts.SuggestedAlternatives) > 0 {
		// The server expects repeated keys; url.Values would be cleaner but the
		// list is sent as a comma-joined value here for parity with the legacy
		// query-string shape.
		joined := ""
		for _, a := range opts.SuggestedAlternatives {
			if joined != "" {
				joined += ","
			}
			joined += a
		}
		params["suggested_alternatives"] = joined
	}
	var resp map[string]any
	if err := i.c.get(ctx, encodeGetURL("/identity/uncertainty", params), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// RegisterLookup registers a lookup table (CSV). Matches Splunk's `lookup`
// semantics.
func (i *IdentityClient) RegisterLookup(ctx context.Context, name, csvData, keyColumn string, opts *RegisterLookupOptions) (map[string]any, error) {
	payload := map[string]any{
		"name":       name,
		"csv_data":   csvData,
		"key_column": keyColumn,
	}
	if opts != nil && len(opts.ValueColumns) > 0 {
		payload["value_columns"] = opts.ValueColumns
	}
	var resp map[string]any
	if err := i.c.postJSON(ctx, "/lookup/register", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListLookups lists registered lookup tables.
func (i *IdentityClient) ListLookups(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := i.c.get(ctx, "/lookup", &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "lookups"), nil
}

// InvokeLookup invokes a registered lookup. Returns the row(s) matching key.
func (i *IdentityClient) InvokeLookup(ctx context.Context, name, key string) (map[string]any, error) {
	var resp map[string]any
	if err := i.c.get(ctx, encodeGetURL("/lookup", map[string]string{"name": name, "key": key}), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// EraseSubject issues "ERASE SUBJECT '<id>' REASON '<r>' [CERTIFY]" via the
// query path. Returns the server's Art. 17 receipt. Pairs with the per-subject
// DEK destroy (#61): today the server does governed-tombstone only.
func (i *IdentityClient) EraseSubject(ctx context.Context, subjectIdentity, reason, purpose string, opts *EraseSubjectOptions) (map[string]any, error) {
	certify := true
	if opts != nil && opts.CertifySet {
		certify = opts.Certify
	}
	certifyKW := ""
	if certify {
		certifyKW = " CERTIFY"
	}
	sql := fmt.Sprintf(
		"ERASE SUBJECT '%s' REASON '%s'%s",
		escapeSQLString(subjectIdentity),
		escapeSQLString(reason),
		certifyKW,
	)
	var resp map[string]any
	if err := i.c.postJSON(ctx, "/query", map[string]any{"purpose": purpose, "sql": sql}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// identifierRE is the allowlist for object-type / column / slot identifiers
// that the SDK interpolates into SQL. Anything that is not a bare identifier is
// rejected client-side (#3211) instead of being pasted into the statement.
var identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateIdentifier reports whether name is a safe SQL identifier (#3211).
// what labels the offending argument in the returned error (e.g. "object_type").
func validateIdentifier(name, what string) error {
	if !identifierRE.MatchString(name) {
		return fmt.Errorf("relata: invalid %s %q: must match ^[A-Za-z_][A-Za-z0-9_]*$", what, name)
	}
	return nil
}

// escapeSQLString escapes a caller-supplied value for interpolation into a SQL
// string literal (#3211). The server lexer (relata_query::parser) terminates a
// literal at the first unescaped quote and honours backslash escapes, so both
// `\` and `'` must be backslash-escaped — the old `'`→`”` doubling neither
// contained a quote correctly nor blocked a `\'` breakout.
func escapeSQLString(s string) string {
	out := make([]byte, 0, len(s)+4)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '\'':
			out = append(out, '\\', s[i])
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
