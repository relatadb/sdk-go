package relata

import (
	"fmt"
	"net/http"
	"strings"
)

// #5689 (owner decision D06): every bulk ingest request carries an
// Idempotency-Key so a retry cannot duplicate keyless rows.
const (
	bulkIngestPath       = "/v1/ingest/bulk"
	headerIdempotencyKey = "Idempotency-Key"
)

// isBulkIngest reports whether the request is a POST /v1/ingest/bulk.
func isBulkIngest(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	p, _, _ := strings.Cut(path, "?")
	return strings.HasSuffix(strings.TrimRight(p, "/"), bulkIngestPath)
}

// withBulkIngestIdempotencyKey returns headers with an automatic
// Idempotency-Key (a random UUID) added when none is present (the Kafka/Stripe
// pattern). It is called once per call, before the retry loop, so every
// attempt reuses the same key and the server replays or converges instead of
// writing twice. An explicit key is never replaced; the input map is not
// modified.
func withBulkIngestIdempotencyKey(headers map[string]string) (map[string]string, error) {
	for k, v := range headers {
		if strings.EqualFold(k, headerIdempotencyKey) && v != "" {
			return headers, nil
		}
	}
	key, err := newRequestID()
	if err != nil {
		return nil, fmt.Errorf("relata: generate idempotency key: %w", err)
	}
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out[headerIdempotencyKey] = key
	return out, nil
}

// retryableStatusFor reports whether a response status is retried. Idempotent
// verbs retry on 502/503/504. A keyed bulk ingest also retries on the gateway
// statuses (502/504), but never on a 503 from Relata itself, which carries its
// own retryable verdict and Retry-After.
func retryableStatusFor(method string, code int, keyedBulk bool) bool {
	if !isRetryableStatus(code) {
		return false
	}
	if isIdempotent(method) {
		return true
	}
	return keyedBulk && code != http.StatusServiceUnavailable
}
