package relata

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// StreamingClient is the synchronous streaming client — NDJSON row streams,
// Arrow IPC byte streams, and SSE consumers (watch / alerts). It backs onto the
// parent client's HTTP transport and purpose context.
type StreamingClient struct {
	c *Client
}

// NewStreamingClient constructs a StreamingClient bound to the parent client.
// Mirrors the Python reference's StreamingClient.from_client.
func NewStreamingClient(c *Client) *StreamingClient {
	return &StreamingClient{c: c}
}

// RowIterator is the NDJSON row-stream iterator. It is the Go analogue of
// Python's query_rows generator. Use Next to advance and Row to read the
// current row; call Close when done (ideally via defer). Err reports any
// decode or transport error encountered after the stream ends.
type RowIterator struct {
	scanner      *bufio.Scanner
	body         io.ReadCloser
	err          error
	row          map[string]any
	ready        bool
	closed       bool
	started      bool
	terminal     bool
	queryID      string
	rowsReceived uint64
	frameLimit   int
}

// IncompleteQueryStreamError means the server did not prove a complete answer.
// RowsReceived rows may already have been consumed; callers must not silently
// retry and concatenate them. QueryID is available after valid leading metadata.
type IncompleteQueryStreamError struct {
	QueryID      string
	RowsReceived uint64
	Reason       string
}

func (e *IncompleteQueryStreamError) Error() string {
	return fmt.Sprintf("relata: incomplete query stream (query_id=%q, rows_received=%d): %s", e.QueryID, e.RowsReceived, e.Reason)
}

func (it *RowIterator) fail(reason string) bool {
	it.err = &IncompleteQueryStreamError{QueryID: it.queryID, RowsReceived: it.rowsReceived, Reason: reason}
	_ = it.Close()
	return false
}

func streamUnsigned(raw json.RawMessage) (uint64, bool) {
	var n uint64
	if len(raw) == 0 || json.Unmarshal(raw, &n) != nil || string(raw) == "null" {
		return 0, false
	}
	return n, true
}

func queryMetadata(row map[string]any) (map[string]any, bool) {
	if len(row) != 1 {
		return nil, false
	}
	meta, ok := row["_meta"].(map[string]any)
	if !ok {
		return nil, false
	}
	for _, key := range []string{"query_id", "started_at", "columns", "processing_time_ms", "row_count", "truncated", "error"} {
		if _, ok := meta[key]; ok {
			return meta, true
		}
	}
	return nil, false
}

// Next yields only row objects. Clean completion requires both metadata frames,
// an exact terminal row count, and truncated:false without an error. It never
// retries. EOF and every framing/transport failure close the response body.
func (it *RowIterator) Next() bool {
	it.ready = false
	if it.closed || it.err != nil {
		return false
	}
	for it.scanner.Scan() {
		raw := it.scanner.Bytes()
		if len(raw) > it.frameLimit {
			return it.fail("NDJSON frame exceeds MaxResponseBytes")
		}
		if !utf8.Valid(raw) {
			return it.fail("NDJSON frame contains invalid UTF-8")
		}
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if json.Unmarshal(line, &row) != nil || row == nil {
			return it.fail("malformed NDJSON object frame")
		}
		if it.terminal {
			return it.fail("data after terminal metadata")
		}
		if meta, ok := queryMetadata(row); ok {
			// Decode numeric metadata separately to avoid float64 rounding of counts.
			var envelope map[string]json.RawMessage
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(line, &envelope)
			_ = json.Unmarshal(envelope["_meta"], &fields)
			if !it.started {
				id, idOK := meta["query_id"].(string)
				columns, columnsOK := meta["columns"].([]any)
				_, timeOK := streamUnsigned(fields["started_at"])
				if !idOK || id == "" || !columnsOK || !timeOK {
					return it.fail("malformed leading metadata")
				}
				for _, column := range columns {
					if _, ok := column.(string); !ok {
						return it.fail("malformed leading columns")
					}
				}
				it.queryID, it.started = id, true
				continue
			}
			count, countOK := streamUnsigned(fields["row_count"])
			_, timeOK := streamUnsigned(fields["processing_time_ms"])
			truncated, truncOK := meta["truncated"].(bool)
			if !countOK || !timeOK || !truncOK {
				return it.fail("malformed terminal metadata")
			}
			if _, exists := fields["error"]; exists {
				return it.fail("server reported an incomplete answer")
			}
			if truncated {
				return it.fail("server truncated the answer")
			}
			if count != it.rowsReceived {
				return it.fail("terminal row count differs from received rows")
			}
			it.terminal = true
			continue
		}
		if !it.started {
			return it.fail("missing leading metadata")
		}
		it.rowsReceived++
		it.row, it.ready = row, true
		return true
	}
	if it.scanner.Err() != nil {
		return it.fail("NDJSON transport or frame-size failure")
	}
	if !it.terminal {
		return it.fail("missing terminal completion metadata")
	}
	if err := it.Close(); err != nil {
		it.err = err
	}
	return false
}

// Row returns the row at the current iterator position. The returned map is
// shared; callers must copy it if they retain it across Next calls.
func (it *RowIterator) Row() map[string]any { return it.row }

// Err returns any error encountered after Next returned false. A nil Err after
// a complete iteration means the stream ended cleanly.
func (it *RowIterator) Err() error { return it.err }

// Close releases the underlying response body. Safe to call multiple times.
func (it *RowIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	if it.body != nil {
		return it.body.Close()
	}
	return nil
}

// QueryRows streams query results as NDJSON rows. The returned iterator must be
// closed by the caller (use defer it.Close()). On a non-2xx response the error
// is returned immediately and no iterator is produced.
//
// A purpose must be resolvable: either passed via StreamingQueryOptions.Purpose
// or inherited from the parent client's DefaultPurpose.
func (s *StreamingClient) QueryRows(ctx context.Context, sql string, opts *StreamingQueryOptions) (*RowIterator, error) {
	purpose, err := s.resolvePurpose(opts)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"purpose": purpose, "sql": sql})
	rc, err := s.c.openStreamAccept(ctx, http.MethodPost, "/v1/query/stream", body, defaultContentType, "application/x-ndjson")
	if err != nil {
		return nil, err
	}
	return newRowIterator(rc, s.c.maxResponseBytes), nil
}

// MaxResponseBytes is a per-frame cap for query NDJSON (default 64 MiB),
// not a total-stream cap. Other streaming APIs retain their existing behavior.
func newRowIterator(body io.ReadCloser, limit int64) *RowIterator {
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	maxInt := int(^uint(0) >> 1)
	if limit >= int64(maxInt) {
		limit = int64(maxInt - 1)
	}
	frameLimit := int(limit)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, min(64*1024, frameLimit+1)), frameLimit+1)
	return &RowIterator{scanner: scanner, body: body, frameLimit: frameLimit}
}

// BytesIterator is the raw Arrow IPC byte-stream iterator from POST /v1/query/arrow.
// Each chunk is a slice of bytes; callers wrap with their Arrow IPC reader.
type BytesIterator struct {
	reader *bufio.Reader
	body   io.ReadCloser
	err    error
	closed bool
}

// Next reads the next chunk of raw bytes. It returns false when the stream is
// exhausted or an error occurred (inspect via Err).
func (it *BytesIterator) Next() bool {
	if it.closed || it.err != nil {
		return false
	}
	return true
}

// Read reads up to len(p) bytes from the stream. Returns io.EOF when done.
// This lets callers use the BytesIterator as an io.Reader for Arrow IPC.
func (it *BytesIterator) Read(p []byte) (int, error) {
	if it.closed {
		return 0, io.EOF
	}
	n, err := it.reader.Read(p)
	if err != nil && err != io.EOF {
		it.err = fmt.Errorf("relata: read arrow stream: %w", err)
	}
	return n, err
}

// Err returns any transport error encountered after the stream ended.
func (it *BytesIterator) Err() error { return it.err }

// Close releases the underlying response body. Safe to call multiple times.
func (it *BytesIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	if it.body != nil {
		return it.body.Close()
	}
	return nil
}

// QueryArrowRaw streams the raw Arrow IPC byte stream from POST /v1/query/arrow.
// The returned *BytesIterator satisfies io.Reader; callers wrap it with their
// Arrow IPC stream reader.
func (s *StreamingClient) QueryArrowRaw(ctx context.Context, sql string, opts *StreamingQueryOptions) (*BytesIterator, error) {
	purpose, err := s.resolvePurpose(opts)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"purpose": purpose, "sql": sql})
	rc, err := s.c.openStream(ctx, http.MethodPost, "/v1/query/arrow", body, defaultContentType)
	if err != nil {
		return nil, err
	}
	return &BytesIterator{
		reader: bufio.NewReaderSize(rc, 64*1024),
		body:   rc,
	}, nil
}

// SSEEvent is a single server-sent event parsed from an SSE stream.
type SSEEvent struct {
	// Type is the "event:" field, when present.
	Type string
	// Data is the parsed JSON payload of the "data:" lines, when JSON; else
	// {"raw": "<joined data lines>"}.
	Data map[string]any
}

// SSEIterator is the SSE consumer iterator used by Watch and Alerts. It parses
// the "event: <type>\ndata: <json>" framing per the SSE spec and yields one
// parsed event per server-sent event.
type SSEIterator struct {
	scanner     *bufio.Scanner
	body        io.ReadCloser
	err         error
	event       SSEEvent
	ready       bool
	closed      bool
	reconnect   bool
	retries     int
	maxRetries  int
	baseBackoff time.Duration

	// factory lets the iterator reconnect when the stream drops.
	factory func() (io.ReadCloser, error)
}

// Next advances to the next SSE event. It returns false when the stream is
// exhausted (and no reconnect is configured, or retries are exhausted).
func (it *SSEIterator) Next() bool {
	it.ready = false
	if it.closed || it.err != nil {
		return false
	}
	for {
		var eventType string
		var dataLines []string
		for it.scanner.Scan() {
			line := it.scanner.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				eventType = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(line[len("data:"):]))
			case line == "" && len(dataLines) > 0:
				raw := strings.Join(dataLines, "\n")
				payload := map[string]any{}
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					payload = map[string]any{"raw": raw}
				}
				if eventType != "" {
					payload["event"] = eventType
				}
				it.event = SSEEvent{Type: eventType, Data: payload}
				it.ready = true
				return true
			}
		}
		// Stream ended without an event boundary.
		if err := it.scanner.Err(); err != nil {
			it.err = fmt.Errorf("relata: read sse stream: %w", err)
		}
		if !it.reconnect || it.factory == nil {
			return false
		}
		it.retries++
		if it.retries > it.maxRetries {
			return false
		}
		delay := it.baseBackoff << uint(min(it.retries-1, 5))
		if delay <= 0 {
			delay = it.baseBackoff
		}
		// The factory closure captures the caller's context, so a cancelled
		// context surfaces as a factory error on the next reconnect attempt —
		// no separate ctx.Done select needed here.
		select {
		case <-time.After(delay):
		}
		// Reconnect.
		if it.body != nil {
			it.body.Close()
		}
		rc, err := it.factory()
		if err != nil {
			it.err = err
			return false
		}
		it.body = rc
		it.scanner = bufio.NewScanner(rc)
	}
}

// Event returns the SSE event at the current iterator position.
func (it *SSEIterator) Event() SSEEvent { return it.event }

// Err returns any transport error encountered after the stream ended.
func (it *SSEIterator) Err() error { return it.err }

// Close releases the underlying response body. Safe to call multiple times.
func (it *SSEIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	if it.body != nil {
		return it.body.Close()
	}
	return nil
}

// WatchOptions configures StreamingClient.Watch.
type WatchOptions struct {
	// Purpose is the declared purpose token. Falls back to the parent client's
	// DefaultPurpose when empty.
	Purpose string
	// Reconnect enables exponential-backoff reconnect on disconnect. Default true.
	Reconnect *bool
}

// AlertsOptions configures StreamingClient.Alerts.
type AlertsOptions struct {
	// Reconnect enables exponential-backoff reconnect on disconnect. Default true.
	Reconnect *bool
}

// StreamingQueryOptions carries the per-call purpose override for the row /
// arrow streaming methods.
type StreamingQueryOptions struct {
	// Purpose is the declared purpose token. Falls back to the parent client's
	// DefaultPurpose when empty.
	Purpose string
}

// Watch opens the finite SSE poll consumer for GET /v1/watch/stream. The server
// emits result_changed hashes, gap, and evaluation_failed notifications; it
// does not stream changed row contents. Reconnects poll the same consumptive
// queue for this authenticated principal. Registrations are in-memory and must
// be re-established after a server restart.
func (s *StreamingClient) Watch(ctx context.Context, sql string, opts *WatchOptions) (*SSEIterator, error) {
	purpose, err := s.resolvePurpose(nil)
	if err != nil {
		return nil, err
	}
	reconnect := true
	if opts != nil {
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
		if opts.Reconnect != nil {
			reconnect = *opts.Reconnect
		}
	}
	params := encodeGetURL("/v1/watch/stream", map[string]string{"sql": sql, "purpose": purpose})
	factory := func() (io.ReadCloser, error) {
		return s.c.openStream(ctx, http.MethodGet, params, nil, "")
	}
	rc, err := factory()
	if err != nil {
		return nil, err
	}
	return &SSEIterator{
		scanner:     bufio.NewScanner(rc),
		body:        rc,
		reconnect:   reconnect,
		maxRetries:  5,
		baseBackoff: time.Second,
		factory:     factory,
	}, nil
}

// Alerts opens the SSE consumer for GET /v1/alerts/stream, yielding alert events
// addressed to the caller's principal / tenant.
func (s *StreamingClient) Alerts(ctx context.Context, opts *AlertsOptions) (*SSEIterator, error) {
	reconnect := true
	if opts != nil && opts.Reconnect != nil {
		reconnect = *opts.Reconnect
	}
	factory := func() (io.ReadCloser, error) {
		return s.c.openStream(ctx, http.MethodGet, "/v1/alerts/stream", nil, "")
	}
	rc, err := factory()
	if err != nil {
		return nil, err
	}
	return &SSEIterator{
		scanner:     bufio.NewScanner(rc),
		body:        rc,
		reconnect:   reconnect,
		maxRetries:  5,
		baseBackoff: time.Second,
		factory:     factory,
	}, nil
}

// GraphChangesOptions configures StreamingClient.GraphChanges.
type GraphChangesOptions struct {
	// Type narrows the stream to one object type.
	Type string
	// TenantID must match the caller's authenticated tenant when set.
	TenantID string
	// Reconnect enables exponential-backoff reconnect on disconnect. Default true.
	Reconnect *bool
}

// GraphChanges opens the SSE consumer for GET /v1/graph/changes (real-time
// graph change stream). opts may be nil.
func (s *StreamingClient) GraphChanges(ctx context.Context, opts *GraphChangesOptions) (*SSEIterator, error) {
	reconnect := true
	params := map[string]string{}
	if opts != nil {
		params["type"] = opts.Type
		params["tenant_id"] = opts.TenantID
		if opts.Reconnect != nil {
			reconnect = *opts.Reconnect
		}
	}
	return s.openSSE(ctx, encodeGetURL("/v1/graph/changes", params), reconnect)
}

// A2ATaskStream opens the SSE consumer for GET /v1/a2a/tasks/:id/stream (A2A
// task lifecycle events). A task stream is finite, so reconnect is off unless
// the caller passes a non-nil true.
func (s *StreamingClient) A2ATaskStream(ctx context.Context, taskID string, reconnect *bool) (*SSEIterator, error) {
	rc := reconnect != nil && *reconnect
	return s.openSSE(ctx, "/v1/a2a/tasks/"+pathSegment(taskID)+"/stream", rc)
}

// openSSE builds an SSEIterator over GET path with the shared reconnect policy.
func (s *StreamingClient) openSSE(ctx context.Context, path string, reconnect bool) (*SSEIterator, error) {
	factory := func() (io.ReadCloser, error) {
		return s.c.openStream(ctx, http.MethodGet, path, nil, "")
	}
	rc, err := factory()
	if err != nil {
		return nil, err
	}
	return &SSEIterator{
		scanner:     bufio.NewScanner(rc),
		body:        rc,
		reconnect:   reconnect,
		maxRetries:  5,
		baseBackoff: time.Second,
		factory:     factory,
	}, nil
}

// resolvePurpose returns the effective purpose, returning ErrPurposeRequired when
// neither per-call nor default purpose is set.
func (s *StreamingClient) resolvePurpose(opts *StreamingQueryOptions) (string, error) {
	if opts != nil && opts.Purpose != "" {
		return opts.Purpose, nil
	}
	if s.c.defaultPurpose == "" {
		return "", ErrPurposeRequired
	}
	return s.c.defaultPurpose, nil
}

// openStream dispatches a streaming HTTP request and returns the open response
// body on 2xx, or a classified error on failure. The caller owns the body and
// must Close it. Retry is intentionally NOT applied to streaming requests — the
// body is the response.
func (c *Client) openStream(ctx context.Context, method, path string, body []byte, contentType string) (io.ReadCloser, error) {
	return c.openStreamAccept(ctx, method, path, body, contentType, "")
}

func (c *Client) openStreamAccept(ctx context.Context, method, path string, body []byte, contentType, accept string) (io.ReadCloser, error) {
	if c.destroyed {
		return nil, ErrClientClosed
	}
	url := c.effectiveBaseURL(path) + path
	if err := c.cleartextBearerGuard(url); err != nil {
		return nil, err
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("relata: build request: %w", err)
	}
	if contentType != "" {
		req.Header.Set(headerContentType, contentType)
	}
	c.setHeaders(req)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.classifyTransportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		// Cap the error body so a huge error payload can't OOM the client (#3214).
		data, _ := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
		rid := resp.Header.Get(headerRequestID)
		retryAfter := parseRetryAfter(resp.Header.Get(headerRetryAfter))
		rerr := errorFromStatus(resp.StatusCode, data, rid, retryAfter, path, resp.Header.Get(headerContentType))
		rerr.RateLimitLimit, rerr.RateLimitRemaining, rerr.RateLimitReset = readRateLimitHeaders(resp.Header)
		return nil, rerr
	}
	return resp.Body, nil
}
