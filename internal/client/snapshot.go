package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the snapshot endpoints return.
const (
	// CodeSnapshotNameTaken means the instance or volume already has a snapshot
	// with this name. A snapshot that failed still holds its name.
	CodeSnapshotNameTaken = "SNAPSHOT_NAME_TAKEN"
)

// Snapshot states reported in ObservedState.
const (
	SnapshotActive  = "active"
	SnapshotFailed  = "failed"
	SnapshotDeleted = "deleted"
)

// Snapshot schedule frequencies.
const (
	FrequencyDaily   = "daily"
	FrequencyWeekly  = "weekly"
	FrequencyMonthly = "monthly"
)

// Snapshot is a point-in-time copy of an instance's root disk, a volume or a
// managed database's data disk. Exactly one of InstanceID, VolumeID and
// DatabaseID is set.
type Snapshot struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	InstanceID         *string    `json:"instance_id"`
	VolumeID           *string    `json:"volume_id"`
	DatabaseID         *string    `json:"database_id"`
	SourceInstanceName *string    `json:"source_instance_name"`
	VolumeName         *string    `json:"volume_name"`
	Region             *string    `json:"region"`
	Trigger            string     `json:"trigger"`
	SizeBytes          int64      `json:"size_bytes"`
	DesiredState       string     `json:"desired_state"`
	ObservedState      string     `json:"observed_state"`
	CompletedAt        *time.Time `json:"completed_at"`
	CreatedAt          *time.Time `json:"created_at"`
	UpdatedAt          *time.Time `json:"updated_at"`
}

// SnapshotSchedule is the automatic snapshot setting of an instance or a volume.
type SnapshotSchedule struct {
	InstanceID     *string    `json:"instance_id"`
	VolumeID       *string    `json:"volume_id"`
	Frequency      string     `json:"frequency"`
	RetentionCount int64      `json:"retention_count"`
	Enabled        bool       `json:"enabled"`
	NextRunAt      *time.Time `json:"next_run_at"`
}

// PutSnapshotScheduleRequest sets a schedule. All three fields are always sent, so
// nothing depends on the platform's defaults.
type PutSnapshotScheduleRequest struct {
	Frequency      string `json:"frequency"`
	RetentionCount int64  `json:"retention_count"`
	Enabled        bool   `json:"enabled"`
}

type createSnapshotRequest struct {
	Name string `json:"name"`
}

type restoreSnapshotRequest struct {
	Name             string `json:"name"`
	DiskOfferingSlug string `json:"disk_offering_slug"`
	SizeGB           int64  `json:"size_gb,omitempty"`
}

// CreateInstanceSnapshot starts a snapshot of an instance's root disk. On staging
// it succeeded for a stopped instance and failed for a running one. It is retried
// on gateway errors because the backend replays it on the same Idempotency-Key.
func (c *Client) CreateInstanceSnapshot(ctx context.Context, instanceID, name string) (*OperationReference, error) {
	return c.createSnapshot(ctx, instancePath(instanceID)+"/snapshots", name)
}

// CreateVolumeSnapshot starts a snapshot of a volume. On staging it succeeded for
// a local volume attached to an instance and failed for a detached shared one.
func (c *Client) CreateVolumeSnapshot(ctx context.Context, volumeID, name string) (*OperationReference, error) {
	return c.createSnapshot(ctx, volumePath(volumeID)+"/snapshots", name)
}

func (c *Client) createSnapshot(ctx context.Context, path, name string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, path, createSnapshotRequest{Name: name}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating snapshot %q: %w", name, err)
	}
	return &ref, nil
}

// GetSnapshot returns one snapshot. A deleted snapshot stays readable with
// observed_state "deleted". It returns ErrNotFound if the id never existed.
func (c *Client) GetSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	var snap Snapshot
	if err := c.do(ctx, http.MethodGet, snapshotPath(id), nil, &snap); err != nil {
		return nil, fmt.Errorf("getting snapshot %s: %w", id, err)
	}
	return &snap, nil
}

// ListSnapshots returns every live snapshot, following the cursor.
func (c *Client) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	snaps, err := listAll[Snapshot](ctx, c, "/snapshots")
	if err != nil {
		return nil, fmt.Errorf("listing snapshots: %w", err)
	}
	return snaps, nil
}

// DeleteSnapshot starts deleting a snapshot. A repeated delete returns a new
// operation. It is retried on gateway errors because the public API replays every write on the same Idempotency-Key except ssh-key create and delete and the console.
func (c *Client) DeleteSnapshot(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, snapshotPath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting snapshot %s: %w", id, err)
	}
	return &ref, nil
}

// RestoreSnapshot starts creating a new volume from a snapshot. The returned
// operation's ResourceID is the new volume. On staging this failed, even from a
// completed snapshot, leaving a failed volume. It is retried on gateway errors
// because the backend replays it on the same Idempotency-Key.
func (c *Client) RestoreSnapshot(ctx context.Context, id, name, diskOfferingSlug string, sizeGB int64) (*OperationReference, error) {
	var ref OperationReference
	req := restoreSnapshotRequest{Name: name, DiskOfferingSlug: diskOfferingSlug, SizeGB: sizeGB}
	if err := c.do(ctx, http.MethodPost, snapshotPath(id)+"/restore", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("restoring snapshot %s: %w", id, err)
	}
	return &ref, nil
}

// GetInstanceSnapshotSchedule returns an instance's schedule, or ErrNotFound when
// it has none.
func (c *Client) GetInstanceSnapshotSchedule(ctx context.Context, instanceID string) (*SnapshotSchedule, error) {
	return c.getSchedule(ctx, instancePath(instanceID)+"/snapshot-schedule")
}

// GetVolumeSnapshotSchedule returns a volume's schedule, or ErrNotFound when it has none.
func (c *Client) GetVolumeSnapshotSchedule(ctx context.Context, volumeID string) (*SnapshotSchedule, error) {
	return c.getSchedule(ctx, volumePath(volumeID)+"/snapshot-schedule")
}

// PutInstanceSnapshotSchedule creates or replaces an instance's schedule. The
// call is synchronous and an upsert.
func (c *Client) PutInstanceSnapshotSchedule(ctx context.Context, instanceID string, req PutSnapshotScheduleRequest) (*SnapshotSchedule, error) {
	return c.putSchedule(ctx, instancePath(instanceID)+"/snapshot-schedule", req)
}

// PutVolumeSnapshotSchedule creates or replaces a volume's schedule.
func (c *Client) PutVolumeSnapshotSchedule(ctx context.Context, volumeID string, req PutSnapshotScheduleRequest) (*SnapshotSchedule, error) {
	return c.putSchedule(ctx, volumePath(volumeID)+"/snapshot-schedule", req)
}

func (c *Client) getSchedule(ctx context.Context, path string) (*SnapshotSchedule, error) {
	var sched SnapshotSchedule
	if err := c.do(ctx, http.MethodGet, path, nil, &sched); err != nil {
		return nil, fmt.Errorf("getting snapshot schedule: %w", err)
	}
	return &sched, nil
}

// putSchedule is retried on gateway errors: setting the same schedule twice has
// the same result, so a repeat is harmless.
func (c *Client) putSchedule(ctx context.Context, path string, req PutSnapshotScheduleRequest) (*SnapshotSchedule, error) {
	var sched SnapshotSchedule
	if err := c.do(ctx, http.MethodPut, path, req, &sched, replaySafe()); err != nil {
		return nil, fmt.Errorf("setting snapshot schedule: %w", err)
	}
	return &sched, nil
}

// snapshotPath escapes the id so a malformed value cannot alter the URL path.
func snapshotPath(id string) string {
	return "/snapshots/" + url.PathEscape(id)
}
