package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// maxStorageGB is the zone maximum the API documents (2000 GB, spec 2026-10-05).
// The step above the plan's size (10 GB) and the plan's own minimum are left to
// the API, which knows the plan and zone and answers INVALID_DATABASE_STORAGE.
const maxStorageGB = 2000

var (
	pathStorage    = path.Root("storage_gb")
	pathDataVolume = path.Root("data_volume_size_gb")
)

// ModifyPlan makes storage_gb grow-only. A smaller value is an error at plan
// time, never a replacement: replacing a database destroys its data, and a disk
// cannot shrink. An unset storage_gb keeps the current size. data_volume_size_gb
// is known in the plan unless the size changes or the database is replaced.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return // destroy or create: nothing to compare with
	}
	var configured, stored, storedVolume types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathStorage, &configured)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, pathStorage, &stored)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, pathDataVolume, &storedVolume)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(resp.RequiresReplace) > 0 {
		return // a new database: its size follows the configuration or the plan
	}
	current := stored
	if current.IsNull() || current.IsUnknown() {
		current = storedVolume // state written before storage_gb existed
	}
	if current.IsNull() || current.IsUnknown() {
		return
	}

	planned := current
	if !configured.IsNull() && !configured.IsUnknown() {
		if configured.ValueInt64() < current.ValueInt64() {
			resp.Diagnostics.AddAttributeError(pathStorage, "Database storage can only grow",
				fmt.Sprintf("storage_gb is %d GB, but the database already has %d GB, and a data disk cannot shrink. Set storage_gb to %d or more.\n\n"+
					"The database is not replaced for this, because replacing it would destroy its data. To start again with a smaller disk, take a snapshot or a dump, then replace it on purpose with `terraform apply -replace=<address>`.",
					configured.ValueInt64(), current.ValueInt64(), current.ValueInt64()))
			return
		}
		planned = configured
	}
	if configured.IsUnknown() {
		return // known after apply: leave both unknown
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, pathStorage, planned)...)
	if planned.Equal(current) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, pathDataVolume, storedVolume)...)
	}
}

// resizeStorage grows the data disk to storageGB and waits until the new size
// is in place. The platform only resizes a running database, so a stopped one is
// started first; the caller brings it back to the configured power state.
func (r *Resource) resizeStorage(ctx context.Context, id string, storageGB int64) error {
	if err := r.applyPower(ctx, id, client.DatabaseRunning); err != nil {
		return err
	}
	db, err := r.api.GetDatabase(ctx, id)
	if err != nil {
		return err
	}
	// A resize to this size may already be running, from an apply that was
	// interrupted: wait for it instead of being refused as a second one.
	if hasPending(db, storageGB) {
		err = r.api.WaitUntil(ctx, "database "+id+" storage to grow", r.hasStorage(id, storageGB, true))
	} else if db.DataVolumeSizeGB < storageGB {
		ref, err := r.api.ResizeDatabaseStorage(ctx, id, storageGB)
		if err != nil {
			return err
		}
		err = r.api.WaitForOperation(ctx, ref.OperationID, r.hasStorage(id, storageGB, false))
		if err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	// The operation can succeed before the database reports the new size, or
	// without it: the size is the authority.
	return r.api.WaitUntil(ctx, "database "+id+" storage to grow", r.hasStorage(id, storageGB, true))
}

func hasPending(db *client.Database, storageGB int64) bool {
	return db.PendingDataVolumeSizeGB != nil && *db.PendingDataVolumeSizeGB == storageGB
}

// hasStorage finishes a resize when the data disk has the new size and no
// resize is pending. A failed database ends the wait with its failure code.
// With settled, a disk that is still small with nothing in flight means the
// resize ended without growing it. That is only known once the resize has been
// seen in flight or its operation has finished: straight after the request the
// database may not show it yet.
func (r *Resource) hasStorage(id string, storageGB int64, settled bool) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		db, err := r.api.GetDatabase(ctx, id)
		if err != nil {
			return false, err
		}
		if db.ObservedState == client.DatabaseFailed {
			return false, failedError(db)
		}
		idle := db.PendingDataVolumeSizeGB == nil
		if settled && idle && db.DataVolumeSizeGB < storageGB {
			return false, errors.New(storageNotGrownText(db, storageGB))
		}
		return idle && db.DataVolumeSizeGB >= storageGB, nil
	}
}

func storageNotGrownText(db *client.Database, storageGB int64) string {
	return fmt.Sprintf("the storage of database %s is still %d GB, not %d GB: the resize ended without growing it, and the size and the bill are unchanged. Apply again to retry", db.ID, db.DataVolumeSizeGB, storageGB)
}
