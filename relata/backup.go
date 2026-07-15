package relata

import (
	"context"
	"fmt"
	"time"
)

// BackupRef is a backup snapshot reference returned by the admin/backup routes.
type BackupRef struct {
	// SnapshotID is the server-assigned snapshot identifier.
	SnapshotID string `json:"snapshot_id"`
	// Kind is "full" or "incremental".
	Kind string `json:"kind"`
	// WrittenAtNS is the nanosecond timestamp the snapshot was written.
	WrittenAtNS int64 `json:"written_at_ns"`
	// SizeBytes is the on-disk snapshot size.
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 is the content hash of the snapshot.
	SHA256 string `json:"sha256"`
	// RowCount is the number of rows captured.
	RowCount int64 `json:"row_count"`
	// Path is the storage path of the snapshot.
	Path string `json:"path"`
}

// newBackupRef builds a BackupRef from a decoded server response.
func newBackupRef(raw map[string]any) BackupRef {
	return BackupRef{
		SnapshotID:  stringField(raw, "snapshot_id"),
		Kind:        orDefault(stringField(raw, "kind"), "full"),
		WrittenAtNS: int64Field(raw, "written_at_ns"),
		SizeBytes:   int64Field(raw, "size_bytes"),
		SHA256:      stringField(raw, "sha256"),
		RowCount:    int64Field(raw, "row_count"),
		Path:        stringField(raw, "path"),
	}
}

// RestoreStatus is the status of a restore job.
type RestoreStatus struct {
	// RestoreID is the job identifier.
	RestoreID string `json:"restore_id"`
	// SnapshotID is the source snapshot.
	SnapshotID string `json:"snapshot_id"`
	// Status is "running", "completed", or "failed".
	Status string `json:"status"`
	// RTOActualSecs is the actual recovery time in seconds.
	RTOActualSecs float64 `json:"rto_actual_secs"`
	// Error carries the failure message on a failed restore.
	Error string `json:"error"`
}

// IsCompleted reports whether the restore finished successfully.
func (r RestoreStatus) IsCompleted() bool { return r.Status == "completed" }

// IsFailed reports whether the restore failed.
func (r RestoreStatus) IsFailed() bool { return r.Status == "failed" }

// newRestoreStatus builds a RestoreStatus from a decoded server response.
func newRestoreStatus(raw map[string]any) RestoreStatus {
	return RestoreStatus{
		RestoreID:     stringField(raw, "restore_id"),
		SnapshotID:    stringField(raw, "snapshot_id"),
		Status:        orDefault(stringField(raw, "status"), "unknown"),
		RTOActualSecs: float64Field(raw, "rto_actual_secs"),
		Error:         stringField(raw, "error"),
	}
}

// BackupClient is the synchronous backup / restore / PITR client. All routes
// require admin auth.
type BackupClient struct {
	c *Client
}

// NewBackupClient constructs a BackupClient that inherits the parent client's
// auth and tenant context. Mirrors the Python reference's BackupClient.from_client.
func NewBackupClient(c *Client) *BackupClient {
	return &BackupClient{c: c}
}

// CreateBackupOptions configures BackupClient.Create.
type CreateBackupOptions struct {
	// Kind is "full" (default) or "incremental".
	Kind string
	// Tenant scopes the backup to one tenant when set.
	Tenant string
}

// RestoreOptions configures BackupClient.Restore.
type RestoreOptions struct {
	// Tenant scopes the restore to one tenant when set.
	Tenant string
	// PointInTimeNS performs a point-in-time restore at the given nanosecond
	// timestamp when set.
	PointInTimeNS int64
}

// WaitForRestoreOptions configures BackupClient.WaitForRestore.
type WaitForRestoreOptions struct {
	// TimeoutSecs caps the wait. Defaults to 300.
	TimeoutSecs float64
	// PollIntervalSecs is the poll period. Defaults to 2.
	PollIntervalSecs float64
}

// Create triggers a backup. Returns the BackupRef.
func (b *BackupClient) Create(ctx context.Context, opts *CreateBackupOptions) (BackupRef, error) {
	kind := "full"
	payload := map[string]any{"kind": kind}
	if opts != nil {
		if opts.Kind != "" {
			payload["kind"] = opts.Kind
		}
		if opts.Tenant != "" {
			payload["tenant"] = opts.Tenant
		}
	}
	var resp map[string]any
	if err := b.c.postJSON(ctx, "/admin/backup", payload, &resp); err != nil {
		return BackupRef{}, err
	}
	return newBackupRef(resp), nil
}

// List lists all backup snapshots.
func (b *BackupClient) List(ctx context.Context) ([]BackupRef, error) {
	var resp map[string]any
	if err := b.c.get(ctx, "/admin/backups", &resp); err != nil {
		return nil, err
	}
	rawList, _ := resp["backups"].([]any)
	out := make([]BackupRef, 0, len(rawList))
	for _, item := range rawList {
		if m, ok := item.(map[string]any); ok {
			out = append(out, newBackupRef(m))
		}
	}
	return out, nil
}

// Restore triggers a restore from snapshotID. Returns the restore_id (the job
// runs asynchronously; poll via RestoreStatus).
func (b *BackupClient) Restore(ctx context.Context, snapshotID string, opts *RestoreOptions) (string, error) {
	payload := map[string]any{"snapshot_id": snapshotID}
	if opts != nil {
		if opts.Tenant != "" {
			payload["tenant"] = opts.Tenant
		}
		if opts.PointInTimeNS > 0 {
			payload["point_in_time_ns"] = opts.PointInTimeNS
		}
	}
	var resp map[string]any
	if err := b.c.postJSON(ctx, "/admin/restore", payload, &resp); err != nil {
		return "", err
	}
	return stringField(resp, "restore_id"), nil
}

// RestoreStatus polls a restore job's status.
func (b *BackupClient) RestoreStatus(ctx context.Context, restoreID string) (RestoreStatus, error) {
	var resp map[string]any
	if err := b.c.get(ctx, fmt.Sprintf("/admin/restore/%s", restoreID), &resp); err != nil {
		return RestoreStatus{}, err
	}
	return newRestoreStatus(resp), nil
}

// Compact triggers background compaction of the row store (#967).
func (b *BackupClient) Compact(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := b.c.postJSON(ctx, "/admin/compact", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// WaitForRestore blocks until the restore completes or fails. Returns
// context.DeadlineExceeded (wrapped) if the timeout elapses.
func (b *BackupClient) WaitForRestore(ctx context.Context, restoreID string, opts *WaitForRestoreOptions) (RestoreStatus, error) {
	timeoutSecs := 300.0
	pollSecs := 2.0
	if opts != nil {
		if opts.TimeoutSecs > 0 {
			timeoutSecs = opts.TimeoutSecs
		}
		if opts.PollIntervalSecs > 0 {
			pollSecs = opts.PollIntervalSecs
		}
	}
	deadline := time.Now().Add(time.Duration(timeoutSecs * float64(time.Second)))
	ticker := time.NewTicker(time.Duration(pollSecs * float64(time.Second)))
	defer ticker.Stop()
	for {
		status, err := b.RestoreStatus(ctx, restoreID)
		if err != nil {
			return status, err
		}
		if status.IsCompleted() || status.IsFailed() {
			return status, nil
		}
		if time.Now().After(deadline) {
			return status, fmt.Errorf("relata: restore %s did not complete within %gs", restoreID, timeoutSecs)
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-ticker.C:
		}
	}
}

// stringField pulls a string value from a decoded map, tolerating non-string
// JSON values by formatting them.
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// int64Field pulls an integer value from a decoded map (JSON float64).
func int64Field(m map[string]any, key string) int64 {
	switch n := m[key].(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	default:
		return 0
	}
}

// float64Field pulls a float value from a decoded map (JSON float64).
func float64Field(m map[string]any, key string) float64 {
	switch n := m[key].(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}
