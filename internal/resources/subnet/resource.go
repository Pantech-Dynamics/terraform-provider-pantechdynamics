// Package subnet implements the pantechdynamics_subnet resource.
package subnet

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// idPrefix is the prefix every subnet id carries.
const idPrefix = "snet_"

// subnetAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type subnetAPI interface {
	CreateSubnet(ctx context.Context, networkID string, req client.CreateSubnetRequest) (*client.OperationReference, error)
	GetSubnet(ctx context.Context, id string) (*client.Subnet, error)
	DeleteSubnet(ctx context.Context, id string) (*client.OperationReference, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one subnet.
type Resource struct {
	api subnetAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_subnet.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subnet"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A subnet: a slice of a network's address range. Instances attach to a subnet, and firewall rules apply to it. It has no in-place changes: changing any argument replaces it, which also requires its instances to be removed first.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the subnet, starting with snet_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"network_id": schema.StringAttribute{
				Description:   "Id of the network the subnet belongs to. Changing it replaces the subnet.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.IDPrefix("network", "net_")},
			},
			"name": schema.StringAttribute{
				Description:   "Name of the subnet, up to 255 characters. Changing it replaces the subnet.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.NameLength("subnet", 255)},
			},
			"cidr": schema.StringAttribute{
				Description:   "IPv4 address range of the subnet, for example \"10.0.1.0/24\". It must sit inside the network's range. The platform checks that only while creating, so a range outside the network fails the create. Changing it replaces the subnet.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.IPv4CIDR()},
			},
			"region": schema.StringAttribute{
				Description:   "Region of the subnet, always its network's region.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"zone": schema.StringAttribute{
				Description:   "Zone of the subnet, always its network's zone.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the subnet as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the subnet was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the subnet was last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
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
	api, ok := req.ProviderData.(subnetAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create asks the backend to create the subnet, saves its id straight away, then
// waits for it to become active.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, plan.Timeouts.Create, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	ref, err := r.api.CreateSubnet(ctx, plan.NetworkID.ValueString(), toCreateRequest(plan))
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error creating subnet", err, attributeFor)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, ref.ResourceID))...)

	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsActive(r.status(ref.ResourceID), r.api.GetOperation, "subnet", ref.ResourceID, ref.OperationID)); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating subnet", "subnet", ref.ResourceID, err)
		return
	}
	s, err := r.api.GetSubnet(ctx, ref.ResourceID)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading subnet after the change", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, s))...)
}

// Read refreshes state. A subnet deleted outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	s, err := r.api.GetSubnet(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && s.ObservedState == resourcekit.StateDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading subnet", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, s))...)
}

// Update only runs when the timeouts block changed, because every other argument
// replaces the subnet. It stores the new block and leaves the API alone.
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

// Delete removes the subnet and waits until it is gone. A subnet that is already
// gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, state.Timeouts.Delete, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	ref, err := r.api.DeleteSubnet(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting subnet", err, map[string]string{
			client.CodeInvalidResourceState: "A subnet cannot be deleted while instances are still in it. Remove those first, then try again.",
		})
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsGone(r.status(id))); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting subnet", "subnet", id, err)
	}
}

// ImportState adopts an existing subnet by id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, "subnet", idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// status reads the subnet's state for the shared done checks.
func (r *Resource) status(id string) resourcekit.StatusGetter {
	return func(ctx context.Context) (resourcekit.Status, error) {
		s, err := r.api.GetSubnet(ctx, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		return resourcekit.Status{Observed: s.ObservedState, Desired: s.DesiredState}, nil
	}
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "cidr", "network_id":
		return path.Root(field), true
	}
	return path.Path{}, false
}
