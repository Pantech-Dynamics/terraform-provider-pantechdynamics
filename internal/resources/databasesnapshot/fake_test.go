package databasesnapshot

import (
	"context"
	"fmt"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory snapshotAPI. Snapshots become active at once unless
// endState says otherwise. orphaned hides snapshots from the database's path,
// as after the database is deleted.
type fakeAPI struct {
	snaps []client.Snapshot

	createErr, deleteErr error
	createLands          bool // a failing create still stores the snapshot (a lost response)
	endState             string
	orphaned             bool
	opFailure            *client.Operation

	creates, deletes, lists int
}

func ptr(s string) *string { return &s }

func (f *fakeAPI) find(id string) *client.Snapshot {
	for i := range f.snaps {
		if f.snaps[i].ID == id {
			return &f.snaps[i]
		}
	}
	return nil
}

func (f *fakeAPI) CreateDatabaseSnapshot(_ context.Context, dbID, name string) (*client.OperationReference, error) {
	f.creates++
	if f.createErr != nil && !f.createLands {
		return nil, f.createErr
	}
	state := f.endState
	if state == "" {
		state = client.SnapshotActive
	}
	id := fmt.Sprintf("snap_%d", len(f.snaps)+1)
	f.snaps = append(f.snaps, client.Snapshot{ID: id, Name: name, DatabaseID: ptr(dbID), Region: ptr("af-abj"),
		Trigger: "manual", SizeBytes: 1 << 30, DesiredState: "present", ObservedState: state})
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id}, nil
}

func (f *fakeAPI) GetDatabaseSnapshot(_ context.Context, dbID, id string) (*client.Snapshot, error) {
	s := f.find(id)
	if s == nil || f.orphaned || s.DatabaseID == nil || *s.DatabaseID != dbID {
		return nil, fmt.Errorf("getting: %w", client.ErrNotFound)
	}
	cp := *s
	return &cp, nil
}

func (f *fakeAPI) GetSnapshot(_ context.Context, id string) (*client.Snapshot, error) {
	if s := f.find(id); s != nil {
		cp := *s
		return &cp, nil
	}
	return nil, fmt.Errorf("getting: %w", client.ErrNotFound)
}

func (f *fakeAPI) ListDatabaseSnapshots(_ context.Context, dbID string) ([]client.Snapshot, error) {
	f.lists++
	var out []client.Snapshot
	for _, s := range f.snaps {
		if s.DatabaseID != nil && *s.DatabaseID == dbID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeAPI) DeleteDatabaseSnapshot(_ context.Context, dbID, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	s := f.find(id)
	if s == nil {
		return nil, fmt.Errorf("deleting: %w", client.ErrNotFound)
	}
	s.DesiredState, s.ObservedState = "deleted", client.SnapshotDeleted
	return &client.OperationReference{OperationID: "op_del", ResourceID: id}, nil
}

func (f *fakeAPI) GetOperation(_ context.Context, id string) (*client.Operation, error) {
	if f.opFailure != nil {
		return f.opFailure, nil
	}
	return &client.Operation{ID: id, Status: client.OperationSucceeded}, nil
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	return kittest.Wait(ctx, done, true)
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	return kittest.Wait(ctx, done, true)
}
