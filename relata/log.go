package relata

import (
	"context"
	"encoding/base64"
	"fmt"
)

// LogLeaf is a single ordered integrity-log leaf entry.
type LogLeaf struct {
	// Index is the strictly-monotonic, gap-free leaf index.
	Index int64
	// Data is the decoded leaf payload.
	Data []byte
	// CommitNS is the nanosecond commit timestamp.
	CommitNS int64
}

// LogClient is the synchronous ordered integrity-log client. It wraps the WAL
// leaf append/head/load surface.
type LogClient struct {
	c *Client
}

// NewLogClient constructs a LogClient that inherits the parent client's auth
// and tenant context. Mirrors the Python reference's LogClient.from_client.
func NewLogClient(c *Client) *LogClient {
	return &LogClient{c: c}
}

// LoadLeavesOptions configures LogClient.LoadLeaves.
type LoadLeavesOptions struct {
	// Since is an exclusive lower-bound index — leaves with index > since are
	// returned. Defaults to 0.
	Since int64
	// Limit caps the response. Defaults to 1000.
	Limit int
}

// Append appends data as a new log leaf. Returns (index, commit_ns). The index
// is strictly monotonic and gap-free under the single-writer model. The server
// fsyncs the WAL before returning the index.
func (l *LogClient) Append(ctx context.Context, data []byte) (index int64, commitNS int64, err error) {
	payload := map[string]any{
		"data_b64": base64.StdEncoding.EncodeToString(data),
	}
	var resp map[string]any
	if err := l.c.postJSON(ctx, "/log/leaf", payload, &resp); err != nil {
		return 0, 0, err
	}
	return int64Field(resp, "index"), int64Field(resp, "commit_ns"), nil
}

// Head returns the current write_seq (the highest committed index).
func (l *LogClient) Head(ctx context.Context) (int64, error) {
	var resp map[string]any
	if err := l.c.get(ctx, "/log/head", &resp); err != nil {
		return 0, err
	}
	return int64Field(resp, "head"), nil
}

// LoadLeaves loads leaves in append order. since is an exclusive lower bound;
// limit caps the response.
func (l *LogClient) LoadLeaves(ctx context.Context, opts *LoadLeavesOptions) ([]LogLeaf, error) {
	since := int64(0)
	limit := 1000
	if opts != nil {
		if opts.Since > 0 {
			since = opts.Since
		}
		if opts.Limit > 0 {
			limit = opts.Limit
		}
	}
	params := map[string]string{
		"since": fmt.Sprintf("%d", since),
		"limit": fmt.Sprintf("%d", limit),
	}
	var resp map[string]any
	if err := l.c.get(ctx, encodeGetURL("/log/leaves", params), &resp); err != nil {
		return nil, err
	}
	rawList, _ := resp["leaves"].([]any)
	out := make([]LogLeaf, 0, len(rawList))
	for _, item := range rawList {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		data, _ := base64.StdEncoding.DecodeString(stringField(m, "data_b64"))
		out = append(out, LogLeaf{
			Index:    int64Field(m, "index"),
			Data:     data,
			CommitNS: int64Field(m, "commit_ns"),
		})
	}
	return out, nil
}
