package relata

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// ObservabilityEvent is a single structured event emitted by the
// GET /observe/stream SSE endpoint (PRs #3822 + #3828). The endpoint is opt-in
// via RELATA_OBSERVE_STREAM=on, requires bearer auth, and is tenant-scoped
// server-side. Fields the server may omit are pointers; a nil pointer means
// the field was absent on the wire.
type ObservabilityEvent struct {
	TsNs        int64   `json:"ts_ns"`
	Level       string  `json:"level"`
	Target      string  `json:"target"`
	Kind        string  `json:"kind"`
	Door        *string `json:"door"`
	Purpose     *string `json:"purpose"`
	TenantID    *string `json:"tenant_id"`
	TraceID     *string `json:"trace_id"`
	Stage       *string `json:"stage"`
	SqlTemplate *string `json:"sql_template"`
	Rows        *uint64 `json:"rows"`
	LatencyMs   *uint64 `json:"latency_ms"`
	Error       *string `json:"error"`
	Message     string  `json:"message"`
}

// ObserveStream opens the GET /observe/stream SSE endpoint and returns a
// channel that yields each parsed event. The endpoint is opt-in
// (RELATA_OBSERVE_STREAM=on) and uses the client's existing auth headers and
// tenant context; it is tenant-scoped server-side.
//
// The channel is buffered (16) and closed when the response body is exhausted
// or ctx is cancelled. A line that is not a "data: <json>" SSE payload, or
// whose payload fails to unmarshal, is skipped so a single malformed frame
// never terminates the stream. On a non-2xx HTTP response the classified error
// is returned immediately and no channel is produced; the caller does not need
// to close anything — the background goroutine owns the response body.
func (c *Client) ObserveStream(ctx context.Context) (<-chan ObservabilityEvent, error) {
	rc, err := c.openStream(ctx, http.MethodGet, "/observe/stream", nil, "")
	if err != nil {
		return nil, err
	}
	ch := make(chan ObservabilityEvent, 16)
	go func() {
		defer rc.Close()
		defer close(ch)
		scanner := bufio.NewScanner(rc)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := line[len("data: "):]
			if !strings.HasPrefix(payload, "{") {
				continue
			}
			var evt ObservabilityEvent
			if json.Unmarshal([]byte(payload), &evt) != nil {
				continue
			}
			select {
			case ch <- evt:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
