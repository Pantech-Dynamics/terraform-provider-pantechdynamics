package volume

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// placed is the outcome of ordering a volume: its id, and the operation to follow
// unless it was found after an ambiguous failure (then there is none).
type placed struct {
	VolumeID    string
	OperationID string
}

// placeVolume orders the volume. Two rules keep it safe. A volume with the same
// name is refused up front, so a recovery by name can never adopt the wrong one.
// And an ambiguous failure (the connection dropped, or a 5xx) is resolved by
// asking the backend, never by ordering again.
func placeVolume(ctx context.Context, api volumeAPI, req client.CreateVolumeRequest, sourceSnapshotID string) (placed, error) {
	existing, err := findByName(ctx, api, req.Name)
	if err != nil {
		return placed{}, fmt.Errorf("checking existing volumes: %w", err)
	}
	if existing != nil {
		return placed{}, fmt.Errorf("a volume named %q already exists (%s). Choose another name, or bring the existing volume under Terraform with `terraform import pantechdynamics_volume.<name> %s`", req.Name, existing.ID, existing.ID)
	}

	ref, err := orderVolume(ctx, api, req, sourceSnapshotID)
	if err == nil {
		return placed{VolumeID: ref.ResourceID, OperationID: ref.OperationID}, nil
	}
	if !isAmbiguous(ctx, err) {
		return placed{}, err
	}

	tflog.Warn(ctx, "volume order outcome unknown, looking it up", map[string]any{"name": req.Name})
	found, lookupErr := findByName(ctx, api, req.Name)
	if lookupErr != nil {
		return placed{}, fmt.Errorf("%w; looking the volume up afterwards also failed: %v", err, lookupErr)
	}
	if found == nil {
		return placed{}, err
	}
	return placed{VolumeID: found.ID}, nil
}

// orderVolume sends the order: a fresh volume, or a volume restored from a snapshot.
// Both are retried on gateway errors, because the backend replays each on its
// Idempotency-Key.
func orderVolume(ctx context.Context, api volumeAPI, req client.CreateVolumeRequest, sourceSnapshotID string) (*client.OperationReference, error) {
	if sourceSnapshotID != "" {
		return api.RestoreSnapshot(ctx, sourceSnapshotID, req.Name, req.DiskOfferingSlug, req.SizeGB)
	}
	return api.CreateVolume(ctx, req)
}

// findByName returns the live volume with this exact name, or nil. Deleted
// volumes do not count.
func findByName(ctx context.Context, api volumeAPI, name string) (*client.Volume, error) {
	volumes, err := api.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range volumes {
		if volumes[i].Name == name && volumes[i].ObservedState != client.VolumeDeleted {
			return &volumes[i], nil
		}
	}
	return nil, nil
}

// isAmbiguous reports whether a failed order may still have taken effect. A
// definite answer below 500 (422, 409, ...) means it did not. A cancelled context
// is not ambiguous either, since the user stopped it.
func isAmbiguous(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true // transport error: no response, outcome unknown
}
