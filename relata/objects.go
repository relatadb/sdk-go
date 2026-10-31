package relata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// ObjectClient is the synchronous typed upsert/load client for state objects.
// It wraps the universal upsert path POST /ingest?object_type=<Type> so callers
// do not have to know the ingest envelope shape.
//
// # Read surface
//
// Get reads one object (with its monotonic "_version") through governed
// GET /objects/{type}/{id}. The current server also supports GET /objects/{type}
// (see the TypeScript ObjectClient); its absence here is an SDK surface
// difference. Use Client.Query for a governed scan until a Go list method is
// added.
//
// # Conditional writes and commit bundles (#5766, #5767)
//
// Upsert with UpsertOptions.ExpectedVersion / IfAbsent, Delete with
// DeleteOptions.ExpectedVersion, and Commit are atomic, durable-before-ack and
// fail with ErrPreconditionFailed (RelataError.CurrentVersion) on a lost race.
// BatchUpsert below uses queued POST /ingest, unlike Python/TypeScript's
// POST /ingest/bulk helper: inspect task completion before relying on visibility.
type ObjectClient struct {
	c       *Client
	purpose string
}

// NewObjectClient constructs an ObjectClient that inherits the parent client's
// auth, tenant, and default-purpose context. Mirrors the Python reference's
// ObjectClient.from_client.
func NewObjectClient(c *Client) *ObjectClient {
	return &ObjectClient{c: c, purpose: c.defaultPurpose}
}

// UpsertOptions configures per-call overrides for ObjectClient.Upsert /
// BatchUpsert.
type UpsertOptions struct {
	// Purpose overrides the parent client's default purpose.
	Purpose string
	// Source attaches a provenance _source to every row.
	Source string
	// ExpectedVersion (#5766) makes Upsert a conditional replace: it succeeds
	// only if the object is at this version (If-Match), otherwise it returns an
	// error wrapping ErrPreconditionFailed. Switches Upsert to the atomic
	// PUT /objects/{type}/{id} door; the result carries the new "version".
	ExpectedVersion *int64
	// IfAbsent (#5766) makes Upsert create-only (If-None-Match: *). Mutually
	// exclusive with ExpectedVersion.
	IfAbsent bool
}

// DeleteOptions configures ObjectClient.Delete.
type DeleteOptions struct {
	// Purpose overrides the parent client's default purpose.
	Purpose string
	// ExpectedVersion makes the delete conditional (If-Match).
	ExpectedVersion *int64
}

// CommitOperation is one operation of an atomic Commit bundle (#5767).
type CommitOperation struct {
	// Op is "create", "update" or "delete".
	Op   string `json:"op"`
	Type string `json:"type"`
	ID   string `json:"id"`
	// ExpectedVersion (update/delete) is the "_version" the caller read.
	ExpectedVersion *int64 `json:"expectedVersion,omitempty"`
	// Data (create/update) is the full object.
	Data map[string]any `json:"data,omitempty"`
}

// CommitOptions configures Commit.
type CommitOptions struct {
	// IdempotencyKey makes a retry return the original result.
	IdempotencyKey string
	// Purpose overrides the parent client's default purpose.
	Purpose string
}

// CommitOperationResult is the per-operation result of a Commit.
type CommitOperationResult struct {
	Op      string `json:"op"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// CommitResult is the result of Commit, or of its idempotent replay.
type CommitResult struct {
	CommitID   string                  `json:"commitId"`
	Operations []CommitOperationResult `json:"operations"`
	// Replayed is true when this is the stored result of an earlier identical request.
	Replayed bool `json:"replayed"`
}

// Upsert upserts a single state object. The objectID becomes the row's primary
// key; re-upserting with the same id supersedes bi-temporally.
//
// A schema rejection is NOT returned as an error: POST /ingest is an
// asynchronous queue-ack, so a rejected row yields a nil error and a payload
// like {"rows_queued":0,"rows_rejected":1,"errors":["Type.field: minCount 1
// not met"]}. Callers MUST check rows_rejected / errors on every call. Returns
// the ingest ack (rows_queued, rows_rejected, errors, task_id, ...), not a
// per-row receipt.
func (o *ObjectClient) Upsert(ctx context.Context, objectType, objectID string, fields map[string]any, opts *UpsertOptions) (map[string]any, error) {
	if opts != nil && (opts.ExpectedVersion != nil || opts.IfAbsent) {
		return o.putConditional(ctx, objectType, objectID, fields, opts)
	}
	row := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		row[k] = v
	}
	row["id"] = objectID
	if opts != nil && opts.Source != "" {
		row["_source"] = opts.Source
	}
	body, err := rowToNDJSON([]map[string]any{row})
	if err != nil {
		return nil, err
	}
	return o.postIngest(ctx, objectType, body, "application/x-ndjson", opts)
}

// putConditional is the atomic PUT /objects/{type}/{id} door (#5766).
func (o *ObjectClient) putConditional(ctx context.Context, objectType, objectID string, fields map[string]any, opts *UpsertOptions) (map[string]any, error) {
	headers, err := preconditionHeaders(opts.ExpectedVersion, opts.IfAbsent)
	if err != nil {
		return nil, err
	}
	body := make(map[string]any, len(fields))
	for k, v := range fields {
		if k != "id" {
			body[k] = v
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("relata: marshal request: %w", err)
	}
	var out map[string]any
	path := objectPath(objectType, objectID, o.resolvePurpose(opts.Purpose))
	if err := o.c.doRequestWithHeaders(ctx, http.MethodPut, path, data, defaultContentType, &out, headers); err != nil {
		return nil, err
	}
	return out, nil
}

// Get fetches one object, including its monotonic "_version" (the value
// ExpectedVersion names), via GET /objects/{type}/{id}. It returns (nil, nil)
// when the object does not exist.
func (o *ObjectClient) Get(ctx context.Context, objectType, objectID string) (map[string]any, error) {
	var out map[string]any
	if err := o.c.get(ctx, objectPath(objectType, objectID, ""), &out); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

// Delete tombstones an object via DELETE /objects/{type}/{id}. With
// DeleteOptions.ExpectedVersion it is conditional (If-Match, #5766): a
// different live version returns an error wrapping ErrPreconditionFailed and
// deletes nothing.
func (o *ObjectClient) Delete(ctx context.Context, objectType, objectID string, opts *DeleteOptions) (map[string]any, error) {
	var headers map[string]string
	purpose := ""
	if opts != nil {
		purpose = opts.Purpose
		if opts.ExpectedVersion != nil {
			var err error
			if headers, err = preconditionHeaders(opts.ExpectedVersion, false); err != nil {
				return nil, err
			}
		}
	}
	var out map[string]any
	if err := o.c.doRequestWithHeaders(ctx, http.MethodDelete, objectPath(objectType, objectID, o.resolvePurpose(purpose)), nil, "", &out, headers); err != nil {
		return nil, err
	}
	return out, nil
}

// ChangesOptions selects a page of the object change feed.
type ChangesOptions struct {
	// Cursor is the NextCursor of the previous page (an event id).
	Cursor string
	// Limit is events per page, 1 to 1000 (server default 100). Zero: default.
	Limit int
	// Type restricts the page to one object type.
	Type string
	// Start "latest" with no Cursor skips history.
	Start string
}

// ChangeEvent is one committed object change. It carries identity, version
// and kind, never field values.
type ChangeEvent struct {
	EventID       string `json:"eventId"`
	Seq           int64  `json:"seq"`
	Kind          string `json:"kind"`
	Type          string `json:"type"`
	ID            string `json:"id"`
	Version       int64  `json:"version"`
	CommitID      string `json:"commitId"`
	OpIndex       int    `json:"opIndex"`
	CommittedAtNs int64  `json:"committedAtNs"`
}

// ChangesPage is one page of the object change feed.
type ChangesPage struct {
	FeedID     string        `json:"feedId"`
	Events     []ChangeEvent `json:"events"`
	NextCursor string        `json:"nextCursor"`
	HasMore    bool          `json:"hasMore"`
	Head       int64         `json:"head"`
	Oldest     int64         `json:"oldest"`
	Withheld   int           `json:"withheld"`
}

// Changes reads one page of the object change feed (GET /object-changes,
// #5716). Persist NextCursor only after every event in the page is processed;
// delivery is at-least-once, so dedupe on EventID. A cursor the server can no
// longer serve returns an error for 410 (change-feed-resync-required): re-read
// state, then call again with Start "latest".
func (o *ObjectClient) Changes(ctx context.Context, opts ChangesOptions) (*ChangesPage, error) {
	q := url.Values{}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Type != "" {
		q.Set("type", opts.Type)
	}
	if opts.Start != "" {
		q.Set("start", opts.Start)
	}
	path := "/object-changes"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out ChangesPage
	if err := o.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Commit atomically applies 1..64 create/update/delete operations
// (POST /commit, #5767): all of them become durable and visible, or none do,
// and every ExpectedVersion is checked before anything is written. A failed
// precondition returns an error wrapping ErrPreconditionFailed
// (RelataError.CurrentVersion, RelataError.FailedOperation). A retry with the
// same IdempotencyKey returns the original result with Replayed set.
func (o *ObjectClient) Commit(ctx context.Context, operations []CommitOperation, opts *CommitOptions) (*CommitResult, error) {
	return o.c.Commit(ctx, operations, opts)
}

// Commit is ObjectClient.Commit on the main client.
func (c *Client) Commit(ctx context.Context, operations []CommitOperation, opts *CommitOptions) (*CommitResult, error) {
	payload := map[string]any{"operations": operations}
	purpose := c.defaultPurpose
	if opts != nil {
		if opts.IdempotencyKey != "" {
			payload["idempotencyKey"] = opts.IdempotencyKey
		}
		if opts.Purpose != "" {
			purpose = opts.Purpose
		}
	}
	if purpose != "" {
		payload["purpose"] = purpose
	}
	var out CommitResult
	if err := c.postJSON(ctx, "/commit", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *ObjectClient) resolvePurpose(override string) string {
	if override != "" {
		return override
	}
	return o.purpose
}

func objectPath(objectType, objectID, purpose string) string {
	path := "/objects/" + url.PathEscape(objectType) + "/" + url.PathEscape(objectID)
	if purpose != "" {
		path += "?purpose=" + url.QueryEscape(purpose)
	}
	return path
}

func preconditionHeaders(expected *int64, ifAbsent bool) (map[string]string, error) {
	if expected != nil && ifAbsent {
		return nil, errors.New("relata: ExpectedVersion and IfAbsent are mutually exclusive")
	}
	switch {
	case ifAbsent:
		return map[string]string{"If-None-Match": "*"}, nil
	case expected != nil:
		return map[string]string{"If-Match": fmt.Sprintf("%q", strconv.FormatInt(*expected, 10))}, nil
	}
	return nil, nil
}

// BatchUpsert bulk-upserts. Local encoding errors reject the entire request
// before HTTP. Each row must carry its own "id" key. Per-row
// rejections are reported in the receipt, not as an error — inspect it. Returns
// the bulk receipt (accepted, rejected, write_seq, queue_depth).
func (o *ObjectClient) BatchUpsert(ctx context.Context, objectType string, rows []map[string]any, opts *UpsertOptions) (map[string]any, error) {
	if opts != nil && opts.Source != "" {
		annotated := make([]map[string]any, len(rows))
		for i, r := range rows {
			cp := make(map[string]any, len(r)+1)
			for k, v := range r {
				cp[k] = v
			}
			cp["_source"] = opts.Source
			annotated[i] = cp
		}
		rows = annotated
	}
	body, err := rowToNDJSON(rows)
	if err != nil {
		return nil, err
	}
	return o.postIngest(ctx, objectType, body, "application/x-ndjson", opts)
}

// postIngest sends a raw body to /ingest?object_type=… with the resolved
// purpose, and decodes the JSON receipt.
func (o *ObjectClient) postIngest(ctx context.Context, objectType, body, contentType string, opts *UpsertOptions) (map[string]any, error) {
	purpose := o.purpose
	if opts != nil && opts.Purpose != "" {
		purpose = opts.Purpose
	}
	params := map[string]string{"object_type": objectType}
	if purpose != "" {
		params["purpose"] = purpose
	}
	status, data, _, err := o.c.rawHTTPRequest(ctx, "POST", encodeGetURL("/ingest", params), []byte(body), contentType)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, errorFromStatus(status, data, "", 0, "", "")
	}
	var resp map[string]any
	if len(data) == 0 {
		return resp, nil
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("relata: decode response: %w", err)
	}
	return resp, nil
}

// rowToNDJSON serialises rows as newline-delimited JSON — one row per line.
// json.Encoder.Encode already appends a newline after each row, so the joined
// output matches Python's "\n".join(json.dumps(r) for r in rows).
// Encoding errors reject the entire local request before any HTTP mutation.
func rowToNDJSON(rows []map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for index, r := range rows {
		if err := enc.Encode(r); err != nil {
			// Return no prefix: callers must refuse before any HTTP effect.
			return "", fmt.Errorf("relata: encode NDJSON row %d: %w", index+1, err)
		}
	}
	out := buf.String()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}
