// Package databasesnapshot implements the pantechdynamics_database_snapshot
// resource: a snapshot of a managed database's data disk.
package databasesnapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	databasePrefix = "db_"
	snapshotPrefix = "snap_"

	// A volume snapshot took about a minute on staging; a large data disk takes longer.
	defaultTimeout = 30 * time.Minute
)

// snapshotAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type snapshotAPI interface {
	CreateDatabaseSnapshot(ctx context.Context, databaseID, name string) (*client.OperationReference, error)
	GetDatabaseSnapshot(ctx context.Context, databaseID, snapshotID string) (*client.Snapshot, error)
	ListDatabaseSnapshots(ctx context.Context, databaseID string) ([]client.Snapshot, error)
	DeleteDatabaseSnapshot(ctx context.Context, databaseID, snapshotID string) (*client.OperationReference, error)
	GetSnapshot(ctx context.Context, id string) (*client.Snapshot, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one database snapshot.
type Resource struct {
	api snapshotAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_database_snapshot.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_snapshot"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A snapshot of a managed database's data disk, taken with the engine running. It is crash-consistent only: what the disk held at that instant, as after a power cut, which the engine recovers from on start. It is not an engine-level backup; stop the database first (desired_state = \"stopped\") for a quiesced copy. The database must be running or stopped. " +
			"A snapshot is billed at the storage rate on its size for every started hour until it is deleted, and it outlives its database. Restoring a database from a snapshot is not available yet. A snapshot cannot be changed: any change replaces it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the snapshot, starting with snap_.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"database_id": schema.StringAttribute{
				Description:   "Id of the database whose data disk is copied, from pantechdynamics_database. Changing it replaces the snapshot.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.IDPrefix("database", databasePrefix)},
			},
			"name": schema.StringAttribute{
				Description:   "Name of the snapshot, for example \"pre-upgrade\", 1 to 255 characters and unique for its database. Changing it replaces the snapshot.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.NameLength("snapshot", 255)},
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the snapshot as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"trigger": schema.StringAttribute{
				Description:   "\"manual\" for a snapshot made here.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"size_bytes": schema.Int64Attribute{
				Description: "Storage the snapshot uses, in bytes. Zero until it completes.",
				Computed:    true,
			},
			"region": schema.StringAttribute{
				Description:   "Region the snapshot is stored in.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"completed_at": schema.StringAttribute{
				Description: "When the snapshot completed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the snapshot was requested, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(snapshotAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create takes the snapshot, saves its id straight away, and waits until it is
// active. Saving first means a timeout cannot orphan a snapshot that is billed.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Create, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	dbID := plan.DatabaseID.ValueString()
	taken, err := placeSnapshot(ctx, r.api, dbID, plan.Name.ValueString())
	if err != nil {
		addCreateError(&resp.Diagnostics, err)
		return
	}
	id := taken.SnapshotID
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, id))...)

	done := resourcekit.IsActive(r.status(dbID, id), r.api.GetOperation, "database snapshot", id, taken.OperationID)
	if taken.OperationID != "" {
		err = r.api.WaitForOperation(ctx, taken.OperationID, done)
	} else {
		err = r.api.WaitUntil(ctx, "database snapshot "+id+" to become active", done) // adopted: no operation
	}
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating database snapshot", "database snapshot", id, err)
		r.refresh(ctx, plan, dbID, id, resp)
		return
	}
	r.refresh(ctx, plan, dbID, id, resp)
}

// Read refreshes state. A snapshot that is gone, or reported as deleted, is
// removed from state so the next plan takes it again.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	snap, err := r.get(ctx, state.DatabaseID.ValueString(), state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && snap.ObservedState == client.SnapshotDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading database snapshot", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, snap))...)
}

// Update only runs when the timeouts block changed, because every argument
// replaces the snapshot. It stores the new block and leaves the API alone.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Timeouts = plan.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete removes the snapshot and waits until it is gone. One that is already
// gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, state.Timeouts.Delete, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	dbID, id := state.DatabaseID.ValueString(), state.ID.ValueString()
	ref, err := r.api.DeleteDatabaseSnapshot(ctx, dbID, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error deleting database snapshot", err, nil)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsGone(r.status(dbID, id))); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting database snapshot", "database snapshot", id, err)
	}
}

// ImportState adopts a snapshot by "<database_id>/<snapshot_id>", because the
// API reads a database's snapshot under its database.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	dbID, snapID, ok := strings.Cut(req.ID, "/")
	if !ok || !strings.HasPrefix(dbID, databasePrefix) || !strings.HasPrefix(snapID, snapshotPrefix) {
		resp.Diagnostics.AddError("Invalid database snapshot import id",
			fmt.Sprintf("Expected \"<database_id>/<snapshot_id>\", for example \"db_123/snap_456\", got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database_id"), dbID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), snapID)...)
}

// get reads the snapshot under its database. A snapshot outlives its database,
// so when the database's path no longer finds it, the snapshot is looked up by
// its own id before it is reported gone: dropping a snapshot that still exists
// from state would leave it billed and unmanaged.
func (r *Resource) get(ctx context.Context, dbID, id string) (*client.Snapshot, error) {
	snap, err := r.api.GetDatabaseSnapshot(ctx, dbID, id)
	if !errors.Is(err, client.ErrNotFound) {
		return snap, err
	}
	snap, fallbackErr := r.api.GetSnapshot(ctx, id)
	if fallbackErr != nil {
		return nil, err
	}
	return snap, nil
}

// refresh reads the snapshot after a create and stores it.
func (r *Resource) refresh(ctx context.Context, prev model, dbID, id string, resp *resource.CreateResponse) {
	snap, err := r.get(ctx, dbID, id)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading database snapshot after the change", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(prev, snap))...)
}

// status reads the snapshot's state for the shared done checks.
func (r *Resource) status(dbID, id string) resourcekit.StatusGetter {
	return func(ctx context.Context) (resourcekit.Status, error) {
		s, err := r.get(ctx, dbID, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		return resourcekit.Status{Observed: s.ObservedState, Desired: s.DesiredState}, nil
	}
}

// withTimeout applies the configured timeout, or defaultTimeout, to ctx.
func withTimeout(ctx context.Context, get resourcekit.Timeout, diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, defaultTimeout)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}
