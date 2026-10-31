package relata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

// UploadSession is the server's view of a resumable upload. Offset is the next
// byte the server expects; the staged bytes are durable across server restarts.
type UploadSession struct {
	UploadID      string  `json:"upload_id"`
	Bucket        string  `json:"bucket"`
	Key           string  `json:"key"`
	Size          int64   `json:"size"`
	Offset        int64   `json:"offset"`
	Complete      bool    `json:"complete"`
	SHA256        *string `json:"sha256"`
	ExpiresNS     int64   `json:"expires_ns"`
	MaxChunkBytes int64   `json:"max_chunk_bytes"`
}

// UploadOptions tunes Upload. Bucket and ContentType behave as in FilesOptions.
type UploadOptions struct {
	Bucket      string
	ContentType string
	// ChunkSize is bytes per chunk (default 8 MiB, capped by the server limit).
	ChunkSize int64
	// MaxAttempts is the consecutive retryable failures tolerated (default 5).
	MaxAttempts int
	// UploadID continues an existing session instead of starting a new one.
	UploadID string
	// SHA256 is the whole-file hex digest the server verifies on completion.
	SHA256 string
}

const defaultUploadChunk = 8 * 1024 * 1024

func uploadPath(id, suffix string, offset *int64) string {
	p := "/files/uploads/" + url.PathEscape(id) + suffix
	if offset != nil {
		p += "?offset=" + strconv.FormatInt(*offset, 10)
	}
	return p
}

func (f *FilesClient) sendUpload(ctx context.Context, method, path string, body []byte, contentType string, expected int, headers map[string]string, out any) error {
	var raw rawHTTPResponse
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Accept"] = "application/json"
	if err := f.c.doRequestWithHeaders(ctx, method, path, body, contentType, &raw, headers); err != nil {
		return err
	}
	if raw.StatusCode != expected {
		return fmt.Errorf("relata: Files upload %s expected HTTP %d, got %d; reconcile with UploadStatus", method, expected, raw.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw.Body, out)
}

// StartUpload opens a resumable upload of exactly size bytes. Nothing is visible
// until CompleteUpload; sha256 (hex, optional) is verified by the server.
func (f *FilesClient) StartUpload(ctx context.Context, key string, size int64, opts *UploadOptions) (*UploadSession, error) {
	body := map[string]any{"key": key, "size": size}
	if opts != nil {
		if opts.Bucket != "" {
			body["bucket"] = opts.Bucket
		}
		if opts.ContentType != "" {
			body["content_type"] = opts.ContentType
		}
		if opts.SHA256 != "" {
			body["sha256"] = opts.SHA256
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var session UploadSession
	if err := f.sendUpload(ctx, http.MethodPost, "/files/uploads", payload, "application/json", http.StatusCreated, nil, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// UploadChunk appends data at offset with an integrity digest. A wrong offset is
// a typed 409; read UploadStatus and resume from the reported offset.
func (f *FilesClient) UploadChunk(ctx context.Context, uploadID string, offset int64, data []byte) (*UploadSession, error) {
	sum := sha256.Sum256(data)
	var session UploadSession
	err := f.sendUpload(ctx, http.MethodPut, uploadPath(uploadID, "", &offset), data, "application/octet-stream", http.StatusOK,
		map[string]string{"X-Relata-Chunk-Sha256": hex.EncodeToString(sum[:])}, &session)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// UploadStatus reads the durable next-expected offset of an upload.
func (f *FilesClient) UploadStatus(ctx context.Context, uploadID string) (*UploadSession, error) {
	var session UploadSession
	if err := f.sendUpload(ctx, http.MethodGet, uploadPath(uploadID, "", nil), nil, "", http.StatusOK, nil, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// CompleteUpload verifies and commits the staged bytes. It is never retried
// automatically; after an uncertain outcome check the file before re-uploading.
func (f *FilesClient) CompleteUpload(ctx context.Context, uploadID string) (*FileMetadata, error) {
	var metadata FileMetadata
	if err := f.sendUpload(ctx, http.MethodPost, uploadPath(uploadID, "/complete", nil), nil, "", http.StatusCreated, nil, &metadata); err != nil {
		return nil, err
	}
	return &metadata, nil
}

// AbortUpload discards an upload and its staged data (idempotent).
func (f *FilesClient) AbortUpload(ctx context.Context, uploadID string) error {
	return f.sendUpload(ctx, http.MethodDelete, uploadPath(uploadID, "", nil), nil, "", http.StatusNoContent, nil, nil)
}

func retryableUploadStatus(err error) bool {
	var re *RelataError
	if !errors.As(err, &re) {
		return false
	}
	switch re.StatusCode {
	case 409, 422, 429, 502, 503, 504:
		return true
	}
	return false
}

// Upload sends size bytes from src in resumable chunks, holding one chunk in
// memory. After a retryable failure it re-reads the server offset and resumes,
// up to MaxAttempts consecutive failures; the session is then left in place
// (pass its ID as UploadOptions.UploadID to continue) and the error returned.
func (f *FilesClient) Upload(ctx context.Context, key string, src io.ReaderAt, size int64, opts *UploadOptions) (*FileMetadata, error) {
	o := UploadOptions{}
	if opts != nil {
		o = *opts
	}
	var session *UploadSession
	var err error
	if o.UploadID != "" {
		session, err = f.UploadStatus(ctx, o.UploadID)
	} else {
		session, err = f.StartUpload(ctx, key, size, &o)
	}
	if err != nil {
		return nil, err
	}
	step := o.ChunkSize
	if step <= 0 {
		step = defaultUploadChunk
	}
	if session.MaxChunkBytes > 0 && session.MaxChunkBytes < step {
		step = session.MaxChunkBytes
	}
	attempts := o.MaxAttempts
	if attempts <= 0 {
		attempts = 5
	}
	failures := 0
	for session.Offset < size {
		n := step
		if size-session.Offset < n {
			n = size - session.Offset
		}
		buf := make([]byte, n)
		if _, err := src.ReadAt(buf, session.Offset); err != nil && !(errors.Is(err, io.EOF) && int64(len(buf)) == n) {
			return nil, fmt.Errorf("relata: read upload source at %d: %w", session.Offset, err)
		}
		next, err := f.UploadChunk(ctx, session.UploadID, session.Offset, buf)
		if err == nil {
			session, failures = next, 0
			continue
		}
		failures++
		if !retryableUploadStatus(err) || failures >= attempts {
			return nil, err
		}
		if session, err = f.UploadStatus(ctx, session.UploadID); err != nil {
			return nil, err
		}
	}
	return f.CompleteUpload(ctx, session.UploadID)
}
