package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// rawHTTPResponse selects capped binary decoding within the shared dispatcher.
type rawHTTPResponse struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
}

// FilesClient stores and retrieves tenant-owned bytes through the core Files API.
// Writes are never automatically retried; an uncertain response must be reconciled
// by the caller. This API does not expose immutable versions or conditional writes.
type FilesClient struct{ c *Client }

// NewFilesClient inherits the parent's authentication, tenant, timeout and response cap.
func NewFilesClient(c *Client) *FilesClient { return &FilesClient{c: c} }

// FilesOptions provides optional namespace and upload content type overrides.
type FilesOptions struct {
	// Bucket defaults to the authenticated tenant. Strict tenant mode requires a match.
	Bucket string
	// ContentType applies to Put only; the default is application/octet-stream.
	ContentType string
}

// FileMetadata describes the current file acknowledged by a successful upload.
// ETag identifies content, not an immutable historical version.
type FileMetadata struct {
	Bucket      string  `json:"bucket"`
	Key         string  `json:"key"`
	RowID       *string `json:"row_id"`
	Size        int64   `json:"size"`
	ETag        string  `json:"etag"`
	ContentType string  `json:"content_type"`
	ModifiedNS  int64   `json:"modified_ns"`
	IsFolder    bool    `json:"is_folder"`
	DownloadURL string  `json:"download_url"`
}

// FileContent is the capped binary body and representation metadata returned by Get.
type FileContent struct {
	Bytes       []byte
	ETag        string
	ContentType string
}

func filePath(key string, opts *FilesOptions) string {
	q := url.Values{"key": {key}}
	if opts != nil && opts.Bucket != "" {
		q.Set("bucket", opts.Bucket)
	}
	return "/files/content?" + q.Encode()
}

// Put uploads exact bytes and returns the server's 201 current-file metadata.
// The server limits uploads to 128 MiB; the parent's response cap still applies.
func (f *FilesClient) Put(ctx context.Context, key string, data []byte, opts *FilesOptions) (*FileMetadata, error) {
	contentType := "application/octet-stream"
	if opts != nil && opts.ContentType != "" {
		contentType = opts.ContentType
	}
	var raw rawHTTPResponse
	if err := f.c.doRequestWithHeaders(ctx, http.MethodPut, filePath(key, opts), data, contentType, &raw, nil); err != nil {
		return nil, err
	}
	if raw.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("relata: Files Put expected HTTP 201, got %d; upload outcome requires reconciliation", raw.StatusCode)
	}
	var metadata FileMetadata
	if err := json.Unmarshal(raw.Body, &metadata); err != nil {
		return nil, fmt.Errorf("relata: decode Files Put metadata after HTTP 201: %w", err)
	}
	return &metadata, nil
}

// Get retrieves exact bytes within the parent's MaxResponseBytes limit (64 MiB by default).
// Context cancellation is forwarded to the shared HTTP transport.
func (f *FilesClient) Get(ctx context.Context, key string, opts *FilesOptions) (*FileContent, error) {
	var raw rawHTTPResponse
	if err := f.c.doRequestWithHeaders(ctx, http.MethodGet, filePath(key, opts), nil, "", &raw, map[string]string{"Accept": "application/octet-stream"}); err != nil {
		return nil, err
	}
	if raw.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relata: Files Get expected HTTP 200, got %d", raw.StatusCode)
	}
	return &FileContent{Bytes: raw.Body, ETag: raw.Headers.Get("ETag"), ContentType: raw.Headers.Get("Content-Type")}, nil
}

// Delete acknowledges the server's empty HTTP 204 response; it is not retried automatically.
func (f *FilesClient) Delete(ctx context.Context, key string, opts *FilesOptions) error {
	var raw rawHTTPResponse
	if err := f.c.doRequestWithHeaders(ctx, http.MethodDelete, filePath(key, opts), nil, "", &raw, nil); err != nil {
		return err
	}
	if raw.StatusCode != http.StatusNoContent {
		return fmt.Errorf("relata: Files Delete expected HTTP 204, got %d; deletion outcome requires reconciliation", raw.StatusCode)
	}
	return nil
}
