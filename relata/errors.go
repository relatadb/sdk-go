package relata

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
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

	// ErrNotFound is returned when the server responds with HTTP 404.
	// The requested resource does not exist.
	ErrNotFound = errors.New("relata: resource not found")

	// ErrConflict is returned when the server responds with HTTP 409.
	// A version or uniqueness conflict occurred.
	ErrConflict = errors.New("relata: conflict")

	// ErrValidation is returned when the server responds with HTTP 422.
	// The request body failed server-side validation.
	ErrValidation = errors.New("relata: validation error")

	// ErrRateLimited is returned when the server responds with HTTP 429.
	// The per-principal cost-unit quota or the rate-limit ceiling has been
	// exceeded. The RetryAfter field on *RelataError carries the requested
	// back-off (when the server supplies a Retry-After header).
	ErrRateLimited = errors.New("relata: rate limited or quota exhausted")

	// ErrQuotaExhausted is an alias for ErrRateLimited kept for backward
	// compatibility. New code should prefer ErrRateLimited.
	ErrQuotaExhausted = ErrRateLimited

	// ErrServerError is returned for any HTTP 5xx response that is not retried
	// (or after retries are exhausted).
	ErrServerError = errors.New("relata: server error")

	// ErrConnection is returned when the SDK cannot reach the Relata server
	// after exhausting retries, or when the request times out.
	ErrConnection = errors.New("relata: cannot reach server")
)

// RelataError wraps a server-side error response with HTTP metadata and the
// RFC 7807 problem+json fields. It implements the error interface and unwraps
// to a sentinel error when one is applicable, so errors.Is works across
// wrapping layers.
type RelataError struct {
	// StatusCode is the HTTP status code returned by the server. Zero for
	// client-side errors (e.g. network failures).
	StatusCode int

	// Message is the human-readable error text from the server response body.
	Message string

	// Err is the underlying sentinel error (e.g. ErrUnauthorized), or nil
	// when no sentinel maps to this status code.
	Err error

	// Code is the dotted problem+json code (e.g.
	// "RELATA.QUERY.PURPOSE_REQUIRED") or empty when the server emits the
	// legacy {"error": "..."} shape.
	Code string

	// TypeURL is the RFC 7807 "type" URL linking to the error docs.
	TypeURL string

	// Retryable reports whether the server said the request can be retried.
	Retryable bool

	// RequestID is the X-Request-ID from the response, when available.
	RequestID string

	// RetryAfter is the requested retry back-off, from the Retry-After header
	// or the problem+json retry_after extension. Zero when not supplied.
	RetryAfter time.Duration

	// RateLimitLimit is the X-RateLimit-Limit header — the per-window request
	// budget (#1321). Zero when the server did not emit the header.
	RateLimitLimit int64
	// RateLimitRemaining is the X-RateLimit-Remaining header. Zero when absent.
	RateLimitRemaining int64
	// RateLimitReset is the X-RateLimit-Reset header (unix seconds). Zero when absent.
	RateLimitReset int64
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

// problemShape is the RFC 7807 application/problem+json envelope. Every field
// is optional; the legacy {"error": "..."} shape populates Detail via fallback.
type problemShape struct {
	Status     int         `json:"status"`
	Code       string      `json:"code"`
	Type       string      `json:"type"`
	Title      string      `json:"title"`
	Detail     string      `json:"detail"`
	Error      string      `json:"error"`
	Message    string      `json:"message"`
	Retryable  bool        `json:"retryable"`
	RequestID  string      `json:"request_id"`
	RetryAfter json.Number `json:"retry_after"`
}

// errorFromStatus maps an HTTP status code to the appropriate sentinel error
// and constructs a RelataError. body is the raw response body text; it is
// parsed as RFC 7807 problem+json when possible, otherwise the raw text is used
// as the message. requestID is the X-Request-ID from the response headers;
// retryAfter is the parsed Retry-After header value (zero if absent).
func errorFromStatus(statusCode int, body []byte, requestID string, retryAfter time.Duration) *RelataError {
	var (
		code      string
		typeURL   string
		detail    string
		retryable bool
		rid       = requestID
	)

	var prob problemShape
	if json.Unmarshal(body, &prob) == nil {
		code = prob.Code
		typeURL = prob.Type
		retryable = prob.Retryable
		if prob.Detail != "" {
			detail = prob.Detail
		} else if prob.Error != "" {
			detail = prob.Error
		} else if prob.Message != "" {
			detail = prob.Message
		} else if prob.Title != "" {
			detail = prob.Title
		}
		if prob.RequestID != "" {
			rid = prob.RequestID
		}
		if retryAfter == 0 && prob.RetryAfter != "" {
			if secs, err := prob.RetryAfter.Float64(); err == nil {
				retryAfter = time.Duration(secs * float64(time.Second))
			}
		}
	}
	if detail == "" {
		detail = string(body)
	}

	re := &RelataError{
		StatusCode: statusCode,
		Message:    detail,
		Code:       code,
		TypeURL:    typeURL,
		Retryable:  retryable,
		RequestID:  rid,
		RetryAfter: retryAfter,
	}
	switch statusCode {
	case 401:
		re.Err = ErrUnauthorized
	case 403:
		re.Err = ErrForbidden
	case 404:
		re.Err = ErrNotFound
	case 409:
		re.Err = ErrConflict
	case 422:
		re.Err = ErrValidation
	case 429:
		re.Err = ErrRateLimited
	default:
		if statusCode >= 500 {
			re.Err = ErrServerError
		}
	}
	return re
}
