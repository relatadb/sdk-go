package relata

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
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

// RecordUncertaintyOptions configures IdentityClient.RecordUncertainty.
type RecordUncertaintyOptions struct {
	// SuggestedAlternatives lists alternate identity candidates for human review.
	SuggestedAlternatives []string
	// Candidates are "left|right" pairs the server ranks by uncertainty
	// (max 500). Empty means an empty sample.
	Candidates []string
	// K caps how many of the most uncertain candidates are returned. Zero
	// leaves the server default.
	K int
}

// RegisterLookupOptions configures IdentityClient.RegisterLookup.
type RegisterLookupOptions struct {
	// ValueColumns restricts which CSV columns are indexed for retrieval.
	ValueColumns []string
}

// EraseSubjectOptions configures IdentityClient.EraseSubject.
type EraseSubjectOptions struct {
	// Certify adds the CERTIFY keyword for Art. 17 attestation. Defaults to
	// false: CERTIFY is destructive opt-in — omitted or false omits the
	// keyword and the server refuses the erasure with "CERTIFY keyword
	// required to confirm irreversible erasure". Only an explicit
	// Certify=true (with CertifySet) opts into the request, not provider key-destruction certification.
	Certify bool
	// CertifySet reports whether Certify was explicitly set.
	CertifySet bool
}

// Label records a human label on a candidate identity pair: isMatch says
// whether left and right refer to the same entity. The server takes one
// learning step on the caller's tenant resolver weights and persists the
// history. Wraps POST /v1/identity/label with {left, right, is_match}.
func (i *IdentityClient) Label(ctx context.Context, left, right string, isMatch bool) (map[string]any, error) {
	payload := map[string]any{"left": left, "right": right, "is_match": isMatch}
	var resp map[string]any
	if err := i.c.postJSON(ctx, "/v1/identity/label", payload, &resp); err != nil {
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
	if opts != nil && len(opts.Candidates) > 0 {
		params["candidates"] = strings.Join(opts.Candidates, ",")
	}
	if opts != nil && opts.K > 0 {
		params["k"] = strconv.Itoa(opts.K)
	}
	var resp map[string]any
	if err := i.c.get(ctx, encodeGetURL("/v1/identity/uncertainty", params), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

var lookupIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// RegisterLookup registers a lookup table from a CSV file that already exists
// ON THE SERVER, under <data dir>/lookups (files elsewhere, or over 16 MiB,
// are refused). It wraps POST /v1/lookup/register with a text/plain
// "REGISTER LOOKUP <name> FROM '<path>' KEY (<col>) FIELDS (<cols>)"
// statement -- the only body the server accepts; it cannot receive CSV text.
// Returns {registered, rows}.
func (i *IdentityClient) RegisterLookup(ctx context.Context, name, serverPath, keyColumn string, opts *RegisterLookupOptions) (map[string]any, error) {
	cols := []string{keyColumn}
	var values []string
	if opts != nil {
		values = opts.ValueColumns
		cols = append(cols, values...)
	}
	ok := lookupIdent.MatchString(name)
	for _, c := range cols {
		ok = ok && lookupIdent.MatchString(c)
	}
	if !ok {
		return nil, fmt.Errorf("relata: RegisterLookup: name and column names must be identifiers ([A-Za-z_][A-Za-z0-9_]*)")
	}
	if serverPath == "" || strings.ContainsAny(serverPath, "'\r\n") {
		return nil, fmt.Errorf("relata: RegisterLookup: serverPath must be a non-empty path without quotes or newlines")
	}
	stmt := fmt.Sprintf("REGISTER LOOKUP %s FROM '%s' KEY (%s)", name, serverPath, keyColumn)
	if len(values) > 0 {
		stmt += fmt.Sprintf(" FIELDS (%s)", strings.Join(values, ", "))
	}
	var resp map[string]any
	if err := i.c.doRequest(ctx, http.MethodPost, "/v1/lookup/register", []byte(stmt), "text/plain", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListLookups lists registered lookup tables as [{"name": ...}] (the route
// returns {"lookup_tables": [names]}).
func (i *IdentityClient) ListLookups(ctx context.Context) ([]map[string]any, error) {
	var resp map[string]any
	if err := i.c.get(ctx, "/v1/lookup", &resp); err != nil {
		return nil, err
	}
	names, _ := resp["lookup_tables"].([]any)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n})
	}
	return out, nil
}

// InvokeLookup fetches the enrichment row for key in lookup table name.
// Wraps GET /v1/lookup/{name}/{key}; returns {name, key, row} and a not-found
// error when the table or key is absent.
func (i *IdentityClient) InvokeLookup(ctx context.Context, name, key string) (map[string]any, error) {
	var resp map[string]any
	if err := i.c.get(ctx, "/v1/lookup/"+pathSegment(name)+"/"+pathSegment(key), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// EraseSubject issues "ERASE SUBJECT '<id>' REASON '<r>' [CERTIFY]" via the
// query path. Returns the server's Art. 17 receipt. Pairs with the per-subject
// DEK destroy (#61): today the server does governed-tombstone only.
//
// Certify is destructive opt-in: omitted (opts == nil, or CertifySet unset)
// or false omits the CERTIFY keyword and the server refuses the erasure with
// "CERTIFY keyword required to confirm irreversible erasure" — matching the
// engine's own safety rail. Only an explicit Certify=true confirms the
// erasure request with explicit incomplete local receipts. (Historically the omitted default was true,
// which silently confirmed destruction for callers that never considered
// the flag.)
func (i *IdentityClient) EraseSubject(ctx context.Context, subjectIdentity, reason, purpose string, opts *EraseSubjectOptions) (map[string]any, error) {
	certify := false
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
	if err := i.c.postJSON(ctx, "/v1/query", map[string]any{"purpose": purpose, "sql": sql}, &resp); err != nil {
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
