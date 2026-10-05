package snapshot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// fakeAPI is an in-memory snapshotAPI that behaves like the staging platform: a
// name is unique per source and a failed snapshot still holds it, and a snapshot
// of a source in a bad state ends failed.
type fakeAPI struct {
	snaps []client.Snapshot

	createErr, listErr, getErr, deleteErr, untilErr error
	createLands, opStuck                            bool
	failNext                                        bool // the next snapshot ends failed, as one of a running instance did

	pendingOpErr error // returned by the next WaitForOperation

	nextID, creates, deletes int
}

var errConnReset = errors.New("connection reset by peer")

func ptr(s string) *string { return &s }

func stamp() *time.Time {
	t := time.Date(2026, 10, 4, 13, 36, 50, 0, time.UTC)
	return &t
}

func snap(id, name, instanceID, volumeID string) client.Snapshot {
	s := client.Snapshot{ID: id, Name: name, Trigger: "manual", SizeBytes: 196928, DesiredState: "present", ObservedState: client.SnapshotActive, CreatedAt: stamp(), CompletedAt: stamp()}
	if instanceID != "" {
		s.InstanceID = ptr(instanceID)
	}
	if volumeID != "" {
		s.VolumeID = ptr(volumeID)
	}
	return s
}

func (f *fakeAPI) create(instanceID, volumeID, name string) (*client.OperationReference, error) {
	f.creates++
	if f.createErr != nil && !f.createLands {
		return nil, f.createErr
	}
	for _, s := range f.snaps {
		sameSource := (instanceID != "" && s.InstanceID != nil && *s.InstanceID == instanceID) || (volumeID != "" && s.VolumeID != nil && *s.VolumeID == volumeID)
		if sameSource && s.Name == name && s.ObservedState != client.SnapshotDeleted {
			return nil, &client.APIError{Status: 409, Code: client.CodeSnapshotNameTaken}
		}
	}
	f.nextID++
	id := fmt.Sprintf("snap_%d", f.nextID)
	s := snap(id, name, instanceID, volumeID)
	if f.failNext {
		s.ObservedState, s.SizeBytes, s.CompletedAt = client.SnapshotFailed, 0, nil
		f.failNext = false
		f.pendingOpErr = &client.OperationError{Operation: client.Operation{ID: "op_snap", Kind: "create_snapshot", Status: "failed",
			Failure: &client.OperationFailure{Code: "PROVISIONING_JOB_FAILED", Reason: "the provider reported job failure"}}}
	}
	f.snaps = append(f.snaps, s)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &client.OperationReference{OperationID: "op_snap", ResourceID: id, Status: client.OperationSubmitting}, nil
}

func (f *fakeAPI) CreateInstanceSnapshot(_ context.Context, instanceID, name string) (*client.OperationReference, error) {
	return f.create(instanceID, "", name)
}

func (f *fakeAPI) CreateVolumeSnapshot(_ context.Context, volumeID, name string) (*client.OperationReference, error) {
	return f.create("", volumeID, name)
}

func (f *fakeAPI) GetSnapshot(_ context.Context, id string) (*client.Snapshot, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.snaps {
		if f.snaps[i].ID == id {
			s := f.snaps[i]
			return &s, nil
		}
	}
	return nil, fmt.Errorf("getting snapshot: %w", client.ErrNotFound)
}

// ListSnapshots leaves out deleted snapshots, as the real list does.
func (f *fakeAPI) ListSnapshots(context.Context) ([]client.Snapshot, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var live []client.Snapshot
	for _, s := range f.snaps {
		if s.ObservedState != client.SnapshotDeleted {
			live = append(live, s)
		}
	}
	return live, nil
}

func (f *fakeAPI) DeleteSnapshot(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.snaps {
		if f.snaps[i].ID == id {
			f.snaps[i].ObservedState, f.snaps[i].DesiredState = client.SnapshotDeleted, "deleted"
			return &client.OperationReference{OperationID: "op_delete", ResourceID: id}, nil
		}
	}
	return nil, fmt.Errorf("deleting snapshot: %w", client.ErrNotFound)
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.pendingOpErr != nil {
		err := f.pendingOpErr
		f.pendingOpErr = nil
		return err
	}
	return f.settle(ctx, done)
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.untilErr != nil {
		return f.untilErr
	}
	return f.settle(ctx, done)
}

func (f *fakeAPI) settle(ctx context.Context, done client.DoneCheck) error {
	if done != nil {
		reached, err := done(ctx)
		if err != nil {
			return err
		}
		if reached {
			return nil
		}
	}
	if f.opStuck {
		return errors.New("never finished and the done check did not pass")
	}
	return nil
}
