// Package network implements the pantechdynamics_network resource.
package network

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

// idPrefix is the prefix every network id carries.
const idPrefix = "net_"

// networkAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type networkAPI interface {
	CreateNetwork(ctx context.Context, req client.CreateNetworkRequest) (*client.OperationReference, error)
	GetNetwork(ctx context.Context, id string) (*client.Network, error)
	DeleteNetwork(ctx context.Context, id string) (*client.OperationReference, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one VPC network.
type Resource struct {
	api networkAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_network.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A private network (VPC). Subnets live inside it, and instances attach to a subnet. A network is billed monthly, and it takes one public address from the zone's pool, so creating one fails with IP_POOL_EXHAUSTED when the pool is empty. It has no in-place changes: changing any argument replaces it, which also requires its subnets, public IPs and instances to be removed first.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the network, starting with net_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Name of the network, up to 255 characters. Names are not unique on the platform. Changing it replaces the network.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.NameLength("network", 255)},
			},
			"cidr": schema.StringAttribute{
				Description:   "IPv4 address range of the network, for example \"10.0.0.0/16\". Subnets must sit inside it. Changing it replaces the network.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.IPv4CIDR()},
			},
			"region": schema.StringAttribute{
				Description: "Region to create the network in, for example \"af-abj\". Omit it for the account's default region. Changing it replaces the network.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"zone": schema.StringAttribute{
				Description:   "Zone the platform placed the network in, for example \"af-abj-2\".",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the network as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the network was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the network was last changed, in RFC 3339 UTC, to the second.",
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
	api, ok := req.ProviderData.(networkAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create asks the backend to create the network, saves its id straight away,
// then waits for it to become active.
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

	ref, err := r.api.CreateNetwork(ctx, toCreateRequest(plan))
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error creating network", err, attributeFor)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, ref.ResourceID))...)

	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsActive(r.status(ref.ResourceID), r.api.GetOperation, "network", ref.ResourceID, ref.OperationID)); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating network", "network", ref.ResourceID, err)
		return
	}
	n, err := r.api.GetNetwork(ctx, ref.ResourceID)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading network after the change", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, n))...)
}

// Read refreshes state. A network deleted outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	n, err := r.api.GetNetwork(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && n.ObservedState == resourcekit.StateDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading network", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, n))...)
}

// Update only runs when the timeouts block changed, because every other argument
// replaces the network. It stores the new block and leaves the API alone.
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

// Delete removes the network and waits until it is gone. A network that is
// already gone counts as success.
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
	ref, err := r.api.DeleteNetwork(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting network", err, map[string]string{
			client.CodeInvalidResourceState: "A network cannot be deleted while it still has instances or public IPs. Remove those first, then try again.",
		})
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsGone(r.status(id))); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting network", "network", id, err)
	}
}

// ImportState adopts an existing network by id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, "network", idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// status reads the network's state for the shared done checks.
func (r *Resource) status(id string) resourcekit.StatusGetter {
	return func(ctx context.Context) (resourcekit.Status, error) {
		n, err := r.api.GetNetwork(ctx, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		return resourcekit.Status{Observed: n.ObservedState, Desired: n.DesiredState}, nil
	}
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "cidr", "region":
		return path.Root(field), true
	}
	return path.Path{}, false
}
