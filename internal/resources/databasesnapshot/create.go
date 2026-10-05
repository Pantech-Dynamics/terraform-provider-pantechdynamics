package databasesnapshot

import (
	"context"
	"errors"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// taken is the outcome of asking for a snapshot: its id, and the operation to
// follow unless it was found after an ambiguous failure (then there is none).
type taken struct {
	SnapshotID  string
	OperationID string
}

// placeSnapshot asks for the snapshot. The create replays on its
// Idempotency-Key and names are unique per database, so an ambiguous failure
// (the connection dropped, or a 5xx) is resolved by looking the snapshot up by
// name, never by asking again.
func placeSnapshot(ctx context.Context, api snapshotAPI, dbID, name string) (taken, error) {
	ref, err := api.CreateDatabaseSnapshot(ctx, dbID, name)
	if err == nil {
		return taken{SnapshotID: ref.ResourceID, OperationID: ref.OperationID}, nil
	}
	if !isAmbiguous(ctx, err) {
		return taken{}, err
	}
	tflog.Warn(ctx, "database snapshot outcome unknown, looking it up", map[string]any{"database_id": dbID, "name": name})
	snaps, lookupErr := api.ListDatabaseSnapshots(ctx, dbID)
	if lookupErr != nil {
		return taken{}, err
	}
	for _, s := range snaps {
		if s.Name == name && s.ObservedState != client.SnapshotDeleted {
			return taken{SnapshotID: s.ID}, nil
		}
	}
	return taken{}, err
}

// isAmbiguous reports whether a failed request may still have taken effect. A
// definite answer below 500 means it did not.
func isAmbiguous(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	if field == "name" {
		return path.Root("name"), true
	}
	return path.Path{}, false
}

// addCreateError shows the API's message and, for the refusals a user can act
// on, what to do.
func addCreateError(diags *diag.Diagnostics, err error) {
	resourcekit.AddHintedAPIError(diags, "Error creating database snapshot", err, attributeFor, map[string]string{
		client.CodeSnapshotNameTaken:  "This database already has, or had, a snapshot with this name. Choose another name, or bring the existing snapshot under Terraform with `terraform import`.",
		"DATABASE_SNAPSHOT_NOT_READY": "A database can be snapshotted only while it is running or stopped. Wait for it to settle, then apply again.",
	})
}
