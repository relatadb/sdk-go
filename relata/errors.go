package relata

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by Client methods.
// Use errors.Is to test for these conditions:
//
//	if errors.Is(err, relata.ErrPurposeRequired) { ... }
var (
	// ErrPurposeRequired is returned when a query is executed without a purpose
	// token. Every Relata query must declare a purpose registered in the
	// tenant's PurposeRegistry. This is enforced both client-side (when
	// DefaultPurpose is empty and no WithPurpose option is supplied) and
	// server-side (the wire rejects purposeless queries).
	ErrPurposeRequired = errors.New("relata: purpose is required for all queries")

	// ErrQuotaExhausted is returned when the server responds with HTTP 429.
	// The per-principal cost-unit quota configured via RELATA_QUERY_QUOTA has
	// been exceeded. Back off and retry, or contact your administrator to raise
	// the quota ceiling.
	ErrQuotaExhausted = errors.New("relata: query quota exhausted")

	// ErrUnauthorized is returned when the server responds with HTTP 401.
	// The bearer token is missing, malformed, or has been revoked.
	ErrUnauthorized = errors.New("relata: invalid or missing bearer token")

	// ErrForbidden is returned when the server responds with HTTP 403.
	// The principal's Cedar ACL policy denies access to the requested data or
	// operation. Check purpose registration and compartment membership.
	ErrForbidden = errors.New("relata: access denied by policy")

	// ErrChainCorrupted is returned when AuditCount reports ChainValid == false.
	// This indicates tampering or storage corruption in the audit trail and must
	// be treated as a security event.
	ErrChainCorrupted = errors.New("relata: audit chain integrity failure")
)

// RelataError wraps a server-side error response with HTTP metadata.
// It implements the error interface and unwraps to a sentinel error when one
// is applicable.
type RelataError struct {
	// StatusCode is the HTTP status code returned by the server.
	StatusCode int

	// Message is the human-readable error text from the server response body.
	Message string

	// Err is the underlying sentinel error (e.g. ErrUnauthorized), or nil
	// when no sentinel maps to this status code.
	Err error
}

// Error implements the error interface.
func (e *RelataError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s (HTTP %d: %s)", e.Err.Error(), e.StatusCode, e.Message)
	}
	return fmt.Sprintf("relata: HTTP %d: %s", e.StatusCode, e.Message)
}

// Unwrap returns the underlying sentinel error so that errors.Is works
// correctly across wrapping layers.
func (e *RelataError) Unwrap() error {
	return e.Err
}

// errorFromStatus maps an HTTP status code to the appropriate sentinel error
// and constructs a RelataError. body is the raw response body text used for
// the human-readable message.
func errorFromStatus(statusCode int, body string) *RelataError {
	var sentinel error
	switch statusCode {
	case 401:
		sentinel = ErrUnauthorized
	case 403:
		sentinel = ErrForbidden
	case 429:
		sentinel = ErrQuotaExhausted
	}
	return &RelataError{
		StatusCode: statusCode,
		Message:    body,
		Err:        sentinel,
	}
}
