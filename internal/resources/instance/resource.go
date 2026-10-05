// Package instance implements the pantechdynamics_instance resource.
package instance

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

const (
	// idPrefix is the prefix every instance id carries.
	idPrefix = "vm_"

	// Provisioning took about 40 seconds on dev but the platform is not always
	// that quick, and delete took about 2.5 minutes.
	defaultCreateTimeout = 30 * time.Minute
	defaultUpdateTimeout = 10 * time.Minute
	defaultDeleteTimeout = 30 * time.Minute
)

// instanceAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type instanceAPI interface {
	CreateInstance(ctx context.Context, req client.CreateInstanceRequest) (*client.InstanceOrderReference, error)
	GetInstance(ctx context.Context, id string) (*client.Instance, error)
	ListInstances(ctx context.Context) ([]client.Instance, error)
	RenameInstance(ctx context.Context, id, name string) (*client.OperationReference, error)
	StartInstance(ctx context.Context, id string) (*client.OperationReference, error)
	StopInstance(ctx context.Context, id string) (*client.OperationReference, error)
	ResizeInstance(ctx context.Context, id, planSlug string) (*client.OperationReference, error)
	ChangeInstanceSecurityGroup(ctx context.Context, id, securityGroupID string) (*client.OperationReference, error)
	DeleteInstance(ctx context.Context, id string) (*client.OperationReference, error)
	WaitForInstanceOrder(ctx context.Context, id string) (*client.InstanceOrder, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one instance.
type Resource struct {
	api instanceAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_instance.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A virtual machine. Creating one places an order that is paid from account credit or the default card, so it costs money. The name, power state, plan (bigger only) and security group can be changed in place, and a plan or security group change restarts the instance. Changing the image, SSH key, region or tags replaces the instance, which destroys its disk.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the instance, starting with vm_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description: "Name of the instance, which becomes its hostname: 1 to 63 letters, digits or hyphens, not starting or ending with a hyphen. Should be unique, because the API accepts a duplicate name and then fails the order. Can be changed in place.",
				Required:    true,
				Validators:  []validator.String{nameValidator{}},
			},
			"plan_slug": schema.StringAttribute{
				Description: "Plan to use, from the pantechdynamics_plans data source. Changing it to a bigger plan resizes the instance in place: it is stopped, resized, which takes several minutes, and restarted, and the disk grows with the plan. Instances only grow, so a smaller plan is refused. To move to a smaller one, replace the instance with `terraform apply -replace`, which destroys its disk.",
				Required:    true,
			},
			"image_slug": schema.StringAttribute{
				Description:   "Operating system image to use, from the pantechdynamics_images data source. Changing it replaces the instance.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"ssh_key_id": schema.StringAttribute{
				Description: "Id of the SSH key to install, from pantechdynamics_ssh_key. The API does not report which key an instance was created with, so Terraform cannot detect a change made outside it, and after an import it holds no value and is not compared. Changing it replaces the instance.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(
					replaceUnlessImported,
					"Replaces the instance when the SSH key changes, but not when the state holds none (after an import).",
					"Replaces the instance when the SSH key changes, but not when the state holds none (after an import).",
				)},
			},
			"region": schema.StringAttribute{
				Description: "Region to create the instance in, for example af-abj. Defaults to the platform's default region. Changing it replaces the instance.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"security_group_id": schema.StringAttribute{
				Description:   "Id of the security group to use, from pantechdynamics_security_group. Defaults to the account's default group, which every such instance shares. Changing it updates the instance in place, but the platform only accepts the change on a stopped instance, so the instance is stopped, switched, and started again if it should be running.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"subnet_id": schema.StringAttribute{
				Description: "Id of the VPC subnet to place the instance in, from pantechdynamics_subnet. A VPC instance has only a private address: attach a pantechdynamics_public_ip to reach it from outside. It cannot use a security group, because the subnet's firewall rules apply instead. Omit it for a standard instance. Changing it replaces the instance.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"network_id": schema.StringAttribute{
				Description: "Id of the VPC network the instance is in. It follows from subnet_id, so it is normally left unset. Changing it replaces the instance.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Free-form key and value labels. Changing them replaces the instance.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
					mapplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"desired_state": schema.StringAttribute{
				Description: "Whether the instance should be \"running\" or \"stopped\". Defaults to \"running\". Changing it starts or stops the instance in place. A stopped instance is billed for storage only. The provider never asks for a state the instance is already in, because the API fails that operation and leaves the instance failed.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(client.InstanceRunning),
				Validators:  []validator.String{oneOf{allowed: []string{client.InstanceRunning, client.InstanceStopped}}},
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the instance as the platform sees it, for example \"running\".",
				Computed:    true,
			},
			"zone": schema.StringAttribute{
				Description:   "Zone the instance runs in, for example af-abj-1.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"public_ipv4": schema.StringAttribute{
				Description: "Public IPv4 address, or null when the instance has none.",
				Computed:    true,
			},
			"private_ipv4": schema.StringAttribute{
				Description: "IPv4 address of the instance on its network.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the instance was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the instance was last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// replaceUnlessImported replaces the instance when the value differs from the
// state, but not when the state holds none. The API never reports the SSH key,
// so an imported instance has none in state, and replacing it for that would
// destroy a machine to fix a gap in our own data.
func replaceUnlessImported(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull() && !req.ConfigValue.Equal(req.StateValue)
}

// Configure receives the API client built by the provider. Terraform calls it
// early with no data, so a nil ProviderData is normal and ignored.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(instanceAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	r.api = api
}

// Create places the order, saves the instance id straight away, then waits for
// the order and for the instance to run. Saving first means a timeout or a
// cancel cannot orphan an order that has already been paid for.
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

	createReq, diags := toCreateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	placed, err := placeOrder(ctx, r.api, createReq)
	if err != nil {
		addCreateError(&resp.Diagnostics, err)
		return
	}
	id := placed.instanceID()
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, id))...)

	if placed.Order != nil {
		if _, err := r.api.WaitForInstanceOrder(ctx, placed.Order.OrderID); err != nil {
			addWaitError(&resp.Diagnostics, "Error creating instance", id, err)
			return
		}
	}
	if err := r.api.WaitUntil(ctx, "instance "+id+" to run", r.isRunning(id)); err != nil {
		addWaitError(&resp.Diagnostics, "Error creating instance", id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() || plan.DesiredState.ValueString() != client.InstanceStopped {
		return
	}

	// An instance is always created running, so a requested stop comes after.
	if err := r.applyPower(ctx, id, client.InstanceStopped); err != nil {
		addWaitError(&resp.Diagnostics, "Error stopping the new instance", id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// Read refreshes state from the API. An instance that is gone, or reported as
// deleted, is removed from state so the next plan recreates it. A deleted
// instance stays readable with observed_state "deleted", unlike most resources.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	inst, err := r.api.GetInstance(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && inst.ObservedState == client.InstanceDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error reading instance", err)
		return
	}

	next, diags := fromAPIResponse(ctx, state, inst)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

// Update applies the in-place changes: rename, security group, plan and power
// state. Every other change replaces the instance.
//
// The order saves downtime. A security group change needs the instance stopped
// and a resize needs it running, so a group change leaves it stopped, a resize
// starts it only because it must, and the last step brings it to the state the
// configuration asks for. State is refreshed after each step, so a step that
// succeeded is not lost if a later one fails.
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
	renamed := !plan.Name.Equal(state.Name)
	groupChanged := knownString(plan.SecurityGroupID) != "" && !plan.SecurityGroupID.Equal(state.SecurityGroupID)
	planChanged := !plan.PlanSlug.Equal(state.PlanSlug)
	powerChanged := !plan.DesiredState.Equal(state.DesiredState)

	if renamed {
		if !r.rename(ctx, id, plan, &resp.State, &resp.Diagnostics) {
			return
		}
	}
	if groupChanged {
		if err := r.changeSecurityGroup(ctx, id, plan.SecurityGroupID.ValueString()); err != nil {
			addUpdateError(&resp.Diagnostics, "Error changing the security group", id, err)
			r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
			return
		}
		r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if planChanged {
		if err := r.resize(ctx, id, plan.PlanSlug.ValueString()); err != nil {
			addUpdateError(&resp.Diagnostics, "Error resizing the instance", id, err)
			r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
			return
		}
		r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	// A group change or a resize moves the instance, so the power state is checked
	// again even when the configuration did not change it.
	if powerChanged || groupChanged || planChanged {
		if err := r.applyPower(ctx, id, plan.DesiredState.ValueString()); err != nil {
			addWaitError(&resp.Diagnostics, "Error changing the instance power state", id, err)
			r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
			return
		}
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// rename renames the instance and saves the result. It reports whether to go on.
func (r *Resource) rename(ctx context.Context, id string, plan model, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) bool {
	ref, err := r.api.RenameInstance(ctx, id, plan.Name.ValueString())
	if err != nil {
		addAPIError(diags, "Error renaming instance", err)
		return false
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.hasName(id, plan.Name.ValueString())); err != nil {
		addWaitError(diags, "Error renaming instance", id, err)
		return false
	}
	r.refresh(ctx, plan, id, state, diags)
	return !diags.HasError()
}

// Delete removes the instance and waits until it is gone. An instance that never
// existed, such as one whose order failed, counts as already deleted.
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
	ref, err := r.api.DeleteInstance(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		addDeleteError(&resp.Diagnostics, err)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.isGone(id)); err != nil {
		addWaitError(&resp.Diagnostics, "Error deleting instance", id, err)
	}
}

// ImportState adopts an existing instance by id. A wrong id fails here with a
// clear message instead of a 404 afterwards.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !strings.HasPrefix(req.ID, idPrefix) {
		resp.Diagnostics.AddError("Invalid instance id", fmt.Sprintf("Expected an id starting with %q, got %q.", idPrefix, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// refresh reads the instance and writes it to state. After an accepted write the
// instance must exist, so a 404 here is an error, not a removal.
func (r *Resource) refresh(ctx context.Context, prev model, id string, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	inst, err := r.api.GetInstance(ctx, id)
	if err != nil {
		addAPIError(diags, "Error reading instance after the change", err)
		return
	}
	next, d := fromAPIResponse(ctx, prev, inst)
	diags.Append(d...)
	diags.Append(state.Set(ctx, next)...)
}

// isRunning finishes a create when the instance runs. A failed instance ends the
// wait at once, with the reason the platform gave.
func (r *Resource) isRunning(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		inst, err := r.api.GetInstance(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		if inst.ObservedState == client.InstanceFailed {
			return false, fmt.Errorf("instance %s failed%s", id, failureText(inst.Failure))
		}
		return inst.ObservedState == client.InstanceRunning, nil
	}
}

// hasName finishes a rename when the instance carries the new name.
func (r *Resource) hasName(id, want string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		inst, err := r.api.GetInstance(ctx, id)
		if err != nil {
			return false, err
		}
		return inst.Name == want, nil
	}
}

// isGone finishes a delete when the instance no longer exists. A deleted
// instance stays readable with observed_state "deleted", so both count.
func (r *Resource) isGone(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		inst, err := r.api.GetInstance(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return inst.ObservedState == client.InstanceDeleted, nil
	}
}

func failureText(f *client.InstanceFailure) string {
	if f == nil {
		return ""
	}
	return ": " + f.Code + ": " + f.Reason
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
