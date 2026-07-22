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
	scanner *bufio.Scanner
	body    io.ReadCloser
	err     error
	row     map[string]any
	ready   bool
	closed  bool
}

// Next advances the iterator to the next row. It returns false when the stream
// is exhausted or an error occurred (inspect via Err).
func (it *RowIterator) Next() bool {
	it.ready = false
	if it.closed || it.err != nil {
		return false
	}
	for it.scanner.Scan() {
		line := strings.TrimSpace(it.scanner.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			it.err = fmt.Errorf("relata: decode ndjson row: %w", err)
			return false
		}
		it.row = m
		it.ready = true
		return true
	}
	if err := it.scanner.Err(); err != nil {
		it.err = fmt.Errorf("relata: read stream: %w", err)
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
	rc, err := s.c.openStream(ctx, http.MethodPost, "/query", body, defaultContentType)
	if err != nil {
		return nil, err
	}
	return &RowIterator{
		scanner: bufio.NewScanner(rc),
		body:    rc,
	}, nil
}

// BytesIterator is the raw Arrow IPC byte-stream iterator from POST /query/arrow.
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

// QueryArrowRaw streams the raw Arrow IPC byte stream from POST /query/arrow.
// The returned *BytesIterator satisfies io.Reader; callers wrap it with their
// Arrow IPC stream reader.
func (s *StreamingClient) QueryArrowRaw(ctx context.Context, sql string, opts *StreamingQueryOptions) (*BytesIterator, error) {
	purpose, err := s.resolvePurpose(opts)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"purpose": purpose, "sql": sql})
	rc, err := s.c.openStream(ctx, http.MethodPost, "/query/arrow", body, defaultContentType)
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

// Watch opens the SSE consumer for GET /watch/stream, yielding RowsAppended
// events as commits matching the watch SQL land.
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
	params := encodeGetURL("/watch/stream", map[string]string{"sql": sql, "purpose": purpose})
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

// Alerts opens the SSE consumer for GET /alerts/stream, yielding alert events
// addressed to the caller's principal / tenant.
func (s *StreamingClient) Alerts(ctx context.Context, opts *AlertsOptions) (*SSEIterator, error) {
	reconnect := true
	if opts != nil && opts.Reconnect != nil {
		reconnect = *opts.Reconnect
	}
	factory := func() (io.ReadCloser, error) {
		return s.c.openStream(ctx, http.MethodGet, "/alerts/stream", nil, "")
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
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("relata: build request: %w", err)
	}
	if contentType != "" {
		req.Header.Set(headerContentType, contentType)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.classifyTransportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		rid := resp.Header.Get(headerRequestID)
		retryAfter := parseRetryAfter(resp.Header.Get(headerRetryAfter))
		rerr := errorFromStatus(resp.StatusCode, data, rid, retryAfter)
		rerr.RateLimitLimit, rerr.RateLimitRemaining, rerr.RateLimitReset = readRateLimitHeaders(resp.Header)
		return nil, rerr
	}
	return resp.Body, nil
}
