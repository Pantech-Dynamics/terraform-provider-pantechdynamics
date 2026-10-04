package snapshot

import (
	"context"
	"errors"
	"net/http"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// placed is the outcome of ordering a snapshot: its id, and the operation to follow
// unless it was found after an ambiguous failure (then there is none).
type placed struct {
	SnapshotID  string
	OperationID string
}

// placeSnapshot orders the snapshot. The create replays on its Idempotency-Key and
// the name is unique per source, so an ambiguous failure (the connection dropped,
// or a 5xx) is resolved by looking the snapshot up by source and name, never by
// ordering again.
func placeSnapshot(ctx context.Context, api snapshotAPI, m model) (placed, error) {
	var (
		ref *client.OperationReference
		err error
	)
	if id := knownString(m.InstanceID); id != "" {
		ref, err = api.CreateInstanceSnapshot(ctx, id, m.Name.ValueString())
	} else {
		ref, err = api.CreateVolumeSnapshot(ctx, knownString(m.VolumeID), m.Name.ValueString())
	}
	if err == nil {
		return placed{SnapshotID: ref.ResourceID, OperationID: ref.OperationID}, nil
	}
	if !isAmbiguous(ctx, err) {
		return placed{}, err
	}

	tflog.Warn(ctx, "snapshot order outcome unknown, looking it up", map[string]any{"name": m.Name.ValueString()})
	found, lookupErr := findBySourceAndName(ctx, api, m)
	if lookupErr != nil || found == nil {
		return placed{}, err
	}
	return placed{SnapshotID: found.ID}, nil
}

// findBySourceAndName returns the live snapshot of this source with this name.
func findBySourceAndName(ctx context.Context, api snapshotAPI, m model) (*client.Snapshot, error) {
	snaps, err := api.ListSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	instanceID, volumeID := knownString(m.InstanceID), knownString(m.VolumeID)
	for i := range snaps {
		s := &snaps[i]
		if s.Name != m.Name.ValueString() || s.ObservedState == client.SnapshotDeleted {
			continue
		}
		if (instanceID != "" && s.InstanceID != nil && *s.InstanceID == instanceID) ||
			(volumeID != "" && s.VolumeID != nil && *s.VolumeID == volumeID) {
			return s, nil
		}
	}
	return nil, nil
}

// isAmbiguous reports whether a failed order may still have taken effect. A
// definite answer below 500 (422, 409, ...) means it did not. A cancelled context is
// not ambiguous either, since the user stopped it.
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
