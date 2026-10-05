package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Volume states reported in ObservedState.
const (
	VolumeActive  = "active"
	VolumeFailed  = "failed"
	VolumeDeleted = "deleted"
)

// Money is an amount in minor currency units (kobo or cents).
type Money struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// Volume is a block storage disk. The fields follow the live API.
type Volume struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	SizeGB               int64      `json:"size_gb"`
	DiskOfferingSlug     string     `json:"disk_offering_slug"`
	StorageType          *string    `json:"storage_type"`
	Region               *string    `json:"region"`
	Zone                 *string    `json:"zone"`
	AttachedInstanceID   *string    `json:"attached_instance_id"`
	AttachedInstanceName *string    `json:"attached_instance_name"`
	DesiredInstanceID    *string    `json:"desired_instance_id"`
	MountPoint           *string    `json:"mount_point"`
	SourceSnapshotID     *string    `json:"source_snapshot_id"`
	MonthlyCost          *Money     `json:"monthly_cost"`
	DesiredState         string     `json:"desired_state"`
	ObservedState        string     `json:"observed_state"`
	CreatedAt            *time.Time `json:"created_at"`
	UpdatedAt            *time.Time `json:"updated_at"`
}

// CreateVolumeRequest orders a volume. SizeGB is needed only for a customized
// disk offering: a fixed offering ignores it silently and uses its own size.
type CreateVolumeRequest struct {
	Name             string `json:"name"`
	DiskOfferingSlug string `json:"disk_offering_slug"`
	SizeGB           int64  `json:"size_gb,omitempty"`
	Region           string `json:"region,omitempty"`
	InstanceID       string `json:"instance_id,omitempty"`
	MountPoint       string `json:"mount_point,omitempty"`
}

// resizeVolumeRequest grows a volume to an offering or, for a customized one, a size.
type resizeVolumeRequest struct {
	DiskOfferingSlug string `json:"disk_offering_slug"`
	SizeGB           int64  `json:"size_gb,omitempty"`
}

// attachVolumeRequest names the instance to attach to.
type attachVolumeRequest struct {
	InstanceID string `json:"instance_id"`
}

// CreateVolume starts creating a volume. The returned operation's ResourceID is
// the volume id. It is retried on gateway errors because the backend replays a
// create on the same Idempotency-Key, verified on staging.
func (c *Client) CreateVolume(ctx context.Context, req CreateVolumeRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, "/volumes", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating volume: %w", err)
	}
	return &ref, nil
}

// GetVolume returns one volume. A deleted volume stays readable with
// observed_state "deleted". It returns ErrNotFound if the id never existed.
func (c *Client) GetVolume(ctx context.Context, id string) (*Volume, error) {
	var vol Volume
	if err := c.do(ctx, http.MethodGet, volumePath(id), nil, &vol); err != nil {
		return nil, fmt.Errorf("getting volume %s: %w", id, err)
	}
	return &vol, nil
}

// ListVolumes returns every live volume, following the cursor.
func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	volumes, err := listAll[Volume](ctx, c, "/volumes")
	if err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	return volumes, nil
}

// ResizeVolume grows a volume to another disk offering. A volume only grows: the
// same or a smaller size is refused with 409 INVALID_RESOURCE_STATE. It is
// retried on gateway errors because the public API replays every write on the same Idempotency-Key except ssh-key create and delete and the console.
func (c *Client) ResizeVolume(ctx context.Context, id, diskOfferingSlug string, sizeGB int64) (*OperationReference, error) {
	var ref OperationReference
	req := resizeVolumeRequest{DiskOfferingSlug: diskOfferingSlug, SizeGB: sizeGB}
	if err := c.do(ctx, http.MethodPost, volumePath(id)+"/resize", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("resizing volume %s: %w", id, err)
	}
	return &ref, nil
}

// AttachVolume starts attaching a volume to a running instance in the same zone.
// It is retried on gateway errors because the backend replays an attach on the
// same Idempotency-Key, verified on staging.
func (c *Client) AttachVolume(ctx context.Context, id, instanceID string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, volumePath(id)+"/attach", attachVolumeRequest{InstanceID: instanceID}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("attaching volume %s: %w", id, err)
	}
	return &ref, nil
}

// DetachVolume starts detaching a volume. Only call it for an attached volume:
// detaching one that is not attached fails the operation after about a minute.
// It is retried on gateway errors because the backend replays a detach on the
// same Idempotency-Key, verified on staging.
func (c *Client) DetachVolume(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, volumePath(id)+"/detach", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("detaching volume %s: %w", id, err)
	}
	return &ref, nil
}

// DeleteVolume starts deleting a volume. An attached volume is refused with 409.
// A deleted volume stays readable as "deleted", and a repeated delete returns a
// new operation. It is retried on gateway errors because the public API replays every write on the same Idempotency-Key except ssh-key create and delete and the console.
func (c *Client) DeleteVolume(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, volumePath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting volume %s: %w", id, err)
	}
	return &ref, nil
}

// volumePath escapes the id so a malformed value cannot alter the URL path.
func volumePath(id string) string {
	return "/volumes/" + url.PathEscape(id)
}
