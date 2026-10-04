// Package snapshotschedule implements the pantechdynamics_snapshot_schedule resource.
package snapshotschedule

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

const defaultRetention = 7

// scheduleAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type scheduleAPI interface {
	GetInstanceSnapshotSchedule(ctx context.Context, instanceID string) (*client.SnapshotSchedule, error)
	GetVolumeSnapshotSchedule(ctx context.Context, volumeID string) (*client.SnapshotSchedule, error)
	PutInstanceSnapshotSchedule(ctx context.Context, instanceID string, req client.PutSnapshotScheduleRequest) (*client.SnapshotSchedule, error)
	PutVolumeSnapshotSchedule(ctx context.Context, volumeID string, req client.PutSnapshotScheduleRequest) (*client.SnapshotSchedule, error)
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages the automatic snapshot schedule of one instance or volume.
type Resource struct {
	api scheduleAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_snapshot_schedule.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot_schedule"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The automatic snapshot schedule of an instance or a volume. Set exactly one of instance_id and volume_id. The API cannot delete a schedule, so destroying this resource pauses it (enabled = false) and leaves it on the platform. Creating it again later replaces that paused schedule. Snapshots a schedule makes are billed for storage like any other, and are not managed by Terraform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The id of the instance or volume the schedule belongs to.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"instance_id": schema.StringAttribute{
				Description:   "Id of the instance whose root disk is snapshotted, from pantechdynamics_instance. Set this or volume_id. Changing it replaces the schedule.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{prefixCheck{prefix: "vm_", kind: "an instance"}},
			},
			"volume_id": schema.StringAttribute{
				Description:   "Id of the volume to snapshot, from pantechdynamics_volume. Set this or instance_id. Changing it replaces the schedule.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{prefixCheck{prefix: "vol_", kind: "a volume"}},
			},
			"frequency": schema.StringAttribute{
				Description: "How often a snapshot is taken: \"daily\", \"weekly\" or \"monthly\". Times are in UTC, and monthly dates clamp to the end of the month.",
				Required:    true,
				Validators:  []validator.String{oneOf{allowed: []string{client.FrequencyDaily, client.FrequencyWeekly, client.FrequencyMonthly}}},
			},
			"retention_count": schema.Int64Attribute{
				Description: "How many successful snapshots are kept, from 1 to 168. Older ones are deleted. Defaults to 7.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(defaultRetention),
				Validators:  []validator.Int64{retentionRange{}},
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether snapshots are taken. Set it to false to pause the schedule. Defaults to true.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"next_run_at": schema.StringAttribute{
				Description: "When the next snapshot is due, in RFC 3339 UTC, to the second. The platform moves it as the schedule runs.",
				Computed:    true,
			},
		},
	}
}

// Configure receives the API client built by the provider. Terraform calls it
// early with no data, so a nil ProviderData is normal and ignored.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(scheduleAPI)
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

// Create sets the schedule. The call is synchronous: when it returns, the schedule exists.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.put(ctx, plan, &resp.State, &resp.Diagnostics)
}

// Update sets the schedule again with the new values.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.put(ctx, plan, &resp.State, &resp.Diagnostics)
}

// put sends the schedule and saves what the platform answers.
func (r *Resource) put(ctx context.Context, plan model, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	id, isInstance := sourceID(plan)
	var (
		sched *client.SnapshotSchedule
		err   error
	)
	if isInstance {
		sched, err = r.api.PutInstanceSnapshotSchedule(ctx, id, toRequest(plan))
	} else {
		sched, err = r.api.PutVolumeSnapshotSchedule(ctx, id, toRequest(plan))
	}
	if err != nil {
		diags.AddError("Error setting the snapshot schedule", err.Error())
		return
	}
	diags.Append(state.Set(ctx, fromAPIResponse(sched))...)
}

// Read refreshes state from the API. A source with no schedule, or one that no
// longer exists, removes the resource from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sched, err := r.get(ctx, state)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading the snapshot schedule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(sched))...)
}

func (r *Resource) get(ctx context.Context, m model) (*client.SnapshotSchedule, error) {
	id, isInstance := sourceID(m)
	if isInstance {
		return r.api.GetInstanceSnapshotSchedule(ctx, id)
	}
	return r.api.GetVolumeSnapshotSchedule(ctx, id)
}

// Delete pauses the schedule, because the API has no way to remove one. A source
// that is already gone, or a schedule that is already paused, needs nothing.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	current, err := r.get(ctx, state)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading the snapshot schedule before pausing it", err.Error())
		return
	}
	if !current.Enabled {
		return
	}

	paused := model{InstanceID: state.InstanceID, VolumeID: state.VolumeID,
		Frequency: types.StringValue(current.Frequency), RetentionCount: types.Int64Value(current.RetentionCount), Enabled: types.BoolValue(false)}
	id, isInstance := sourceID(paused)
	if isInstance {
		_, err = r.api.PutInstanceSnapshotSchedule(ctx, id, toRequest(paused))
	} else {
		_, err = r.api.PutVolumeSnapshotSchedule(ctx, id, toRequest(paused))
	}
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Error pausing the snapshot schedule", err.Error())
	}
}

// ImportState adopts the schedule of an instance or a volume, by that id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	switch {
	case strings.HasPrefix(req.ID, "vm_"):
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), req.ID)...)
	case strings.HasPrefix(req.ID, "vol_"):
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), req.ID)...)
	default:
		resp.Diagnostics.AddError("Invalid id", fmt.Sprintf("Import a schedule by the id of its instance (vm_...) or volume (vol_...), got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
