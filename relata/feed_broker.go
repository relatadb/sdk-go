package relata

import (
	"context"
	"errors"
)

// FeedBrokerClient calls the optional RIFN feed broker at its own explicit
// base URL. It is separate from the database SystemClient.
type FeedBrokerClient struct {
	c *Client
}

// NewFeedBrokerClient constructs a broker client. Pass the broker URL and its
// own credentials/options; this does not inherit a Relata database client.
func NewFeedBrokerClient(brokerURL string, opts *ClientOptions) *FeedBrokerClient {
	return &FeedBrokerClient{c: New(brokerURL, opts)}
}

// Health returns broker liveness and capacity information.
func (f *FeedBrokerClient) Health(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := f.c.get(ctx, "/feed/health", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Channels lists the broker's public channels.
func (f *FeedBrokerClient) Channels(ctx context.Context) ([]map[string]any, error) {
	var resp []map[string]any
	if err := f.c.get(ctx, "/feed/channels", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Publish sends a complete signed FeedEntry. publisherDID must match the
// body's publisher_id; the broker validates the entry and signature.
func (f *FeedBrokerClient) Publish(ctx context.Context, publisherDID string, entry map[string]any) (map[string]any, error) {
	if publisherDID == "" {
		return nil, errors.New("relata: publisherDID is required")
	}
	var resp map[string]any
	if err := f.c.postJSONHeaders(ctx, "/feed/publish", entry, &resp, map[string]string{"X-Publisher-Did": publisherDID}); err != nil {
		return nil, err
	}
	return resp, nil
}

// Close zeroizes the broker credential and closes this client.
func (f *FeedBrokerClient) Close() error { return f.c.Close() }
