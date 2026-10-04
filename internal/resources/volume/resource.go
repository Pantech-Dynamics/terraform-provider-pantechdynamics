// Package volume implements the pantechdynamics_volume resource.
package volume

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
	// idPrefix is the prefix every volume id carries.
	idPrefix = "vol_"

	// Volume operations took about a minute each on staging, and calls are
	// sometimes much slower, so the waits are generous.
	defaultCreateTimeout = 20 * time.Minute
	defaultUpdateTimeout = 30 * time.Minute
	defaultDeleteTimeout = 20 * time.Minute
)

// volumeAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type volumeAPI interface {
	CreateVolume(ctx context.Context, req client.CreateVolumeRequest) (*client.OperationReference, error)
	GetVolume(ctx context.Context, id string) (*client.Volume, error)
	ListVolumes(ctx context.Context) ([]client.Volume, error)
	ResizeVolume(ctx context.Context, id, diskOfferingSlug string, sizeGB int64) (*client.OperationReference, error)
	AttachVolume(ctx context.Context, id, instanceID string) (*client.OperationReference, error)
	DetachVolume(ctx context.Context, id string) (*client.OperationReference, error)
	DeleteVolume(ctx context.Context, id string) (*client.OperationReference, error)
	ListDiskOfferings(ctx context.Context) ([]client.DiskOffering, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
	_ resource.ResourceWithModifyPlan  = &Resource{}
)

// Resource manages one volume.
type Resource struct {
	api volumeAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_volume.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A block storage volume, billed by the hour. It can be attached to one running instance in the same zone. The disk offering can be changed to a bigger one in place, but only while the volume is detached, and a volume never shrinks. Changing the name, region or mount point replaces the volume, which destroys its data.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the volume, starting with vol_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Name of the volume. Should be unique, so the provider refuses a name that is already used. Changing it replaces the volume.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{nameCheck},
			},
			"disk_offering_slug": schema.StringAttribute{
				Description: "The kind of disk, from the pantechdynamics_disk_offerings data source. It sets the size (for a fixed offering) and the storage type, shared or local. Changing it to a bigger offering grows the volume in place while it is detached. It never shrinks.",
				Required:    true,
			},
			"size_gb": schema.Int64Attribute{
				Description:   "Size in gigabytes. Required for a customized offering. A fixed offering has its own size, and setting a different size_gb is an error, because the API would silently ignore it.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{sizeFollowsOffering{}},
			},
			"region": schema.StringAttribute{
				Description: "Region to create the volume in, for example af-abj. Defaults to the platform's default region. Changing it replaces the volume.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "Id of the instance the volume is attached to, from pantechdynamics_instance. The instance must be running and in the volume's zone. Setting it attaches the volume, changing it moves the volume, and removing it detaches the volume. Unmount the disk inside the instance before detaching, or the data may be damaged. Null means not attached. Destroying the volume detaches it first.",
				Optional:    true,
				Validators:  []validator.String{instanceIDCheck},
			},
			"mount_point": schema.StringAttribute{
				Description:   "An absolute path such as /data, stored with the volume as a label. The platform does not mount anything. Changing it replaces the volume.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{mountPointCheck},
			},
			"storage_type": schema.StringAttribute{
				Description:   "\"shared\" or \"local\". Local volumes attached to instances on staging, shared ones did not.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"zone": schema.StringAttribute{
				Description:   "Zone the volume lives in, for example af-abj-1.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the volume as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"monthly_cost_minor": schema.Int64Attribute{
				Description: "Estimated cost for a full month, in minor currency units (for example kobo). An estimate, not a quote.",
				Computed:    true,
			},
			"currency": schema.StringAttribute{
				Description: "Currency of monthly_cost_minor.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the volume was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the volume was last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider. Terraform calls it
// early with no data, so a nil ProviderData is normal and ignored.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(volumeAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	r.api = api
}

// ModifyPlan refuses a resize of an attached volume at plan time. The platform
// answers that with a bare 409, so the user is told before anything runs.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // a create or a destroy
	}
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	stillAttached := !state.InstanceID.IsNull() && plan.InstanceID.Equal(state.InstanceID)
	if resizeNeeded(plan, state) && stillAttached {
		resp.Diagnostics.AddAttributeError(path.Root("disk_offering_slug"), "Detach the volume before resizing it",
			"The platform only resizes a detached volume. Set instance_id to null in the same change to detach it first, unmount the disk inside the instance before that, and set instance_id again afterwards to attach it.")
	}
}

// resizeNeeded reports whether the plan asks for a different offering or size.
func resizeNeeded(plan, state model) bool {
	if !plan.DiskOfferingSlug.Equal(state.DiskOfferingSlug) {
		return true
	}
	return !plan.SizeGB.IsUnknown() && !plan.SizeGB.IsNull() && !plan.SizeGB.Equal(state.SizeGB)
}

// Create checks the offering, orders the volume, saves its id straight away, waits
// for it, and then attaches it if an instance was given. Saving first means a
// timeout or a cancel cannot orphan a volume that is already being billed.
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

	offerings, err := r.api.ListDiskOfferings(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error reading the disk offerings", err)
		return
	}
	sizeSet := !plan.SizeGB.IsNull() && !plan.SizeGB.IsUnknown()
	if _, problem := checkOffering(offerings, plan.DiskOfferingSlug.ValueString(), knownInt(plan.SizeGB), sizeSet); problem != nil {
		resp.Diagnostics.AddAttributeError(path.Root(problem.Field), "Invalid disk offering or size", problem.Message)
		return
	}

	order, err := placeVolume(ctx, r.api, toCreateRequest(plan))
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error creating volume", err)
		return
	}
	id := order.VolumeID
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, id))...)

	if order.OperationID != "" {
		err = r.api.WaitForOperation(ctx, order.OperationID, r.isActive(id))
	} else {
		err = r.api.WaitUntil(ctx, "volume "+id+" to become active", r.isActive(id))
	}
	if err != nil {
		addWaitError(&resp.Diagnostics, "Error creating volume", id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() || knownString(plan.InstanceID) == "" {
		return
	}

	if err := r.attach(ctx, id, plan.InstanceID.ValueString()); err != nil {
		addAttachError(&resp.Diagnostics, id, modelFromState(ctx, resp.State).StorageType.ValueString(), err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// modelFromState reads the model back from state, ignoring errors: it is only used to
// word an error message.
func modelFromState(ctx context.Context, state interface {
	Get(context.Context, any) diag.Diagnostics
}) model {
	var m model
	_ = state.Get(ctx, &m)
	return m
}

// Read refreshes state from the API. A volume that is gone, or reported as
// deleted, is removed from state so the next plan recreates it. A deleted volume
// stays readable with observed_state "deleted".
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vol, err := r.api.GetVolume(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && vol.ObservedState == client.VolumeDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error reading volume", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, vol))...)
}

// Update applies the in-place changes in the one order the platform allows: detach
// first (also when moving to another instance), then resize while detached, then
// attach. State is refreshed after each step, so a step that succeeded is not lost
// if a later one fails.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Update, defaultUpdateTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	current, wanted := knownString(state.InstanceID), knownString(plan.InstanceID)
	step := func(summary string, err error) bool {
		if err == nil {
			return true
		}
		addWaitError(&resp.Diagnostics, summary, id, err)
		r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
		return false
	}

	if current != "" && wanted != current {
		if !step("Error detaching the volume", r.detach(ctx, id)) {
			return
		}
		r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
	}

	if resizeNeeded(plan, state) {
		if err := r.growTo(ctx, id, plan, state); err != nil {
			addResizeError(&resp.Diagnostics, id, err)
			r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
			return
		}
		r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
	}

	if wanted != "" && wanted != current {
		if err := r.attach(ctx, id, wanted); err != nil {
			addAttachError(&resp.Diagnostics, id, state.StorageType.ValueString(), err)
			r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
			return
		}
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// growTo resizes the volume to the plan's offering, after checking that it really
// grows. A volume never shrinks, and the platform answers a shrink with a bare 409.
func (r *Resource) growTo(ctx context.Context, id string, plan, state model) error {
	offerings, err := r.api.ListDiskOfferings(ctx)
	if err != nil {
		return err
	}
	sizeSet := !plan.SizeGB.IsNull() && !plan.SizeGB.IsUnknown()
	off, problem := checkOffering(offerings, plan.DiskOfferingSlug.ValueString(), knownInt(plan.SizeGB), sizeSet)
	if problem != nil {
		return fmt.Errorf("%s", problem.Message)
	}
	target := targetSize(off, knownInt(plan.SizeGB))
	if target <= state.SizeGB.ValueInt64() {
		return fmt.Errorf("%w: the volume is %d GB and the new offering gives %d GB", errShrink, state.SizeGB.ValueInt64(), target)
	}
	sizeArg := int64(0)
	if off.CustomSize {
		sizeArg = target
	}
	return r.resize(ctx, id, off.Slug, sizeArg, target)
}

// errShrink marks a resize that would not grow the volume.
var errShrink = errors.New("volumes only grow")

// addResizeError explains a failed resize.
func addResizeError(diags *diag.Diagnostics, id string, err error) {
	if errors.Is(err, errShrink) {
		diags.AddAttributeError(path.Root("disk_offering_slug"), "Volumes can only grow",
			err.Error()+".\n\nTo get a smaller volume, replace it with `terraform apply -replace=<address>`. That destroys the volume and its data.")
		return
	}
	if client.HasCode(err, client.CodeInvalidResourceState) {
		diags.AddAttributeError(path.Root("disk_offering_slug"), "The volume cannot be resized now",
			err.Error()+"\n\nThe platform only resizes a detached volume, and only to a bigger size.")
		return
	}
	addWaitError(diags, "Error resizing the volume", id, err)
}

// Delete removes the volume. It first detaches an attached volume, and clears the
// stale attach request a failed attach leaves behind, because the platform refuses
// to delete either. A volume that is already gone counts as success.
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
	vol, err := r.api.GetVolume(ctx, id)
	if errors.Is(err, client.ErrNotFound) || (err == nil && vol.ObservedState == client.VolumeDeleted) {
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error reading volume before deleting it", err)
		return
	}
	switch {
	case vol.AttachedInstanceID != nil:
		if err := r.detach(ctx, id); err != nil {
			addWaitError(&resp.Diagnostics, "Error detaching the volume before deleting it", id, err)
			return
		}
	case vol.DesiredInstanceID != nil:
		r.clearAttachIntent(ctx, id)
	}

	ref, err := r.api.DeleteVolume(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		addDeleteError(&resp.Diagnostics, err)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.isGone(id)); err != nil {
		addWaitError(&resp.Diagnostics, "Error deleting volume", id, err)
	}
}

// ImportState adopts an existing volume by id. A wrong id fails here with a clear
// message instead of a 404 afterwards.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !strings.HasPrefix(req.ID, idPrefix) {
		resp.Diagnostics.AddError("Invalid volume id", fmt.Sprintf("Expected an id starting with %q, got %q.", idPrefix, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// refresh reads the volume and writes it to state. After an accepted write the
// volume must exist, so a 404 here is an error, not a removal.
func (r *Resource) refresh(ctx context.Context, prev model, id string, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	vol, err := r.api.GetVolume(ctx, id)
	if err != nil {
		addAPIError(diags, "Error reading volume after the change", err)
		return
	}
	diags.Append(state.Set(ctx, fromAPIResponse(prev, vol))...)
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

// sizeFollowsOffering plans size_gb. When the size is not configured and the
// offering changes, the size will change with it, so it is unknown until the
// resize runs. Otherwise it keeps the state's value, to avoid a noisy diff.
type sizeFollowsOffering struct{}

var _ planmodifier.Int64 = sizeFollowsOffering{}

func (sizeFollowsOffering) Description(context.Context) string {
	return "Keeps the size, except when the disk offering changes and no size is configured."
}

func (m sizeFollowsOffering) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (sizeFollowsOffering) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() {
		return // a create, or a size the user configured
	}
	var plannedOffering, storedOffering types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("disk_offering_slug"), &plannedOffering)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("disk_offering_slug"), &storedOffering)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plannedOffering.Equal(storedOffering) {
		resp.PlanValue = req.StateValue
		return
	}
	resp.PlanValue = types.Int64Unknown()
}
