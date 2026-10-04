// Package snapshot implements the pantechdynamics_snapshot resource.
package snapshot

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
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

const (
	// idPrefix is the prefix every snapshot id carries.
	idPrefix = "snap_"

	// A snapshot took about a minute on staging. A large root disk takes longer.
	defaultCreateTimeout = 30 * time.Minute
	defaultDeleteTimeout = 20 * time.Minute
)

// snapshotAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type snapshotAPI interface {
	CreateInstanceSnapshot(ctx context.Context, instanceID, name string) (*client.OperationReference, error)
	CreateVolumeSnapshot(ctx context.Context, volumeID, name string) (*client.OperationReference, error)
	GetSnapshot(ctx context.Context, id string) (*client.Snapshot, error)
	ListSnapshots(ctx context.Context) ([]client.Snapshot, error)
	DeleteSnapshot(ctx context.Context, id string) (*client.OperationReference, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one snapshot.
type Resource struct {
	api snapshotAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_snapshot.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A point-in-time snapshot of an instance's root disk or of a volume. Set exactly one of instance_id and volume_id. A snapshot cannot be changed: any change replaces it. Snapshot storage is billed monthly by size. On the staging platform, snapshots of a stopped instance and of a local volume attached to an instance succeeded, while snapshots of a running instance and of a detached shared volume failed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the snapshot, starting with snap_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Name of the snapshot, unique for its instance or volume. On the staging platform a snapshot that failed or was deleted keeps its name reserved, so a name cannot be used again. Changing it replaces the snapshot.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{nameCheck},
			},
			"instance_id": schema.StringAttribute{
				Description:   "Id of the instance whose root disk is copied, from pantechdynamics_instance. Stop the instance first: a snapshot of a running instance failed on staging. Set this or volume_id. Changing it replaces the snapshot.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{prefixCheck("vm_", "an instance")},
			},
			"volume_id": schema.StringAttribute{
				Description:   "Id of the volume to copy, from pantechdynamics_volume. Set this or instance_id. Changing it replaces the snapshot.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{prefixCheck("vol_", "a volume")},
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the snapshot as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"trigger": schema.StringAttribute{
				Description:   "\"manual\" for a snapshot made here, or \"automatic\" for one made by a schedule.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"size_bytes": schema.Int64Attribute{
				Description: "Size of the snapshot in bytes. Zero until it completes.",
				Computed:    true,
			},
			"completed_at": schema.StringAttribute{
				Description: "When the snapshot completed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the snapshot was requested, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider. Terraform calls it
// early with no data, so a nil ProviderData is normal and ignored.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(snapshotAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	r.api = api
}

// ValidateConfig requires exactly one source. An unknown value counts as set, so a
// reference to a resource that does not exist yet is accepted.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var instanceID, volumeID types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("instance_id"), &instanceID)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("volume_id"), &volumeID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	set := 0
	if !instanceID.IsNull() {
		set++
	}
	if !volumeID.IsNull() {
		set++
	}
	if set != 1 {
		resp.Diagnostics.AddError("Exactly one source is required", "Set exactly one of instance_id and volume_id.")
	}
}

// Create orders the snapshot, saves its id straight away, and waits until it is
// active. Saving first means a timeout or a cancel cannot orphan a snapshot that is
// already billed for storage.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Create, defaultCreateTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	order, err := placeSnapshot(ctx, r.api, plan)
	if err != nil {
		addCreateError(&resp.Diagnostics, err)
		return
	}
	id := order.SnapshotID
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, id))...)

	if order.OperationID != "" {
		err = r.api.WaitForOperation(ctx, order.OperationID, r.isActive(id))
	} else {
		err = r.api.WaitUntil(ctx, "snapshot "+id+" to become active", r.isActive(id))
	}
	if err != nil {
		addWaitError(&resp.Diagnostics, "Error creating snapshot", id, failureHint(plan), err)
		r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// Read refreshes state from the API. A snapshot that is gone, or reported as
// deleted, is removed from state so the next plan recreates it. A deleted snapshot
// stays readable with observed_state "deleted".
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	snap, err := r.api.GetSnapshot(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && snap.ObservedState == client.SnapshotDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error reading snapshot", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, snap))...)
}

// Update is never reached except to change the timeouts block: every other
// attribute forces a replacement.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.refresh(ctx, plan, plan.ID.ValueString(), &resp.State, &resp.Diagnostics)
}

// Delete removes the snapshot and waits until it is gone. A snapshot that is
// already gone counts as success, and a failed one can be deleted like any other.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, state.Timeouts.Delete, defaultDeleteTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	ref, err := r.api.DeleteSnapshot(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error deleting snapshot", err)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.isGone(id)); err != nil {
		addWaitError(&resp.Diagnostics, "Error deleting snapshot", id, "", err)
	}
}

// ImportState adopts an existing snapshot by id. A wrong id fails here with a clear
// message instead of a 404 afterwards.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !strings.HasPrefix(req.ID, idPrefix) {
		resp.Diagnostics.AddError("Invalid snapshot id", fmt.Sprintf("Expected an id starting with %q, got %q.", idPrefix, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// refresh reads the snapshot and writes it to state. After an accepted write the
// snapshot must exist, so a 404 here is an error, not a removal.
func (r *Resource) refresh(ctx context.Context, prev model, id string, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	snap, err := r.api.GetSnapshot(ctx, id)
	if err != nil {
		addAPIError(diags, "Error reading snapshot after the change", err)
		return
	}
	diags.Append(state.Set(ctx, fromAPIResponse(prev, snap))...)
}

// isActive finishes a create when the snapshot is active. A failed snapshot ends
// the wait at once.
func (r *Resource) isActive(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		snap, err := r.api.GetSnapshot(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		if snap.ObservedState == client.SnapshotFailed {
			return false, fmt.Errorf("snapshot %s ended in the failed state", id)
		}
		return snap.ObservedState == client.SnapshotActive, nil
	}
}

// isGone finishes a delete when the snapshot reads as deleted or no longer exists.
func (r *Resource) isGone(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		snap, err := r.api.GetSnapshot(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return snap.ObservedState == client.SnapshotDeleted, nil
	}
}

// withTimeout applies the configured timeout, or the default, to ctx.
func withTimeout(ctx context.Context, get func(context.Context, time.Duration) (time.Duration, diag.Diagnostics), fallback time.Duration, diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, fallback)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}
