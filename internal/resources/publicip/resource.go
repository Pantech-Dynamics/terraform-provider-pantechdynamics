// Package publicip implements the pantechdynamics_public_ip resource.
package publicip

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "pip_"

	// codeIPPoolExhausted is the operation failure when the region ran out of
	// addresses between the availability check and the request.
	codeIPPoolExhausted = "IP_POOL_EXHAUSTED"
)

// publicIPAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type publicIPAPI interface {
	CreatePublicIP(ctx context.Context, req client.CreatePublicIPRequest) (*client.OperationReference, error)
	GetPublicIP(ctx context.Context, id string) (*client.PublicIP, error)
	DeletePublicIP(ctx context.Context, id string) (*client.OperationReference, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one public IP address.
type Resource struct {
	api publicIPAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_public_ip.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A public IPv4 address on a VPC network, billed monthly. A static_nat address maps every port to one instance. A port_forwarding address carries pantechdynamics_port_forwarding_rule resources. The address opens nothing on its own: the subnet's firewall rules must allow the traffic. It cannot be changed in place, so changing any argument replaces it and the address changes.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the public IP, starting with pip_.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"network_id": schema.StringAttribute{
				Description:   "Id of the VPC network the address is for. Changing it replaces the address.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.IDPrefix("network", "net_")},
			},
			"purpose": schema.StringAttribute{
				Description:   "\"static_nat\" (the default) maps every port of the address to one instance and needs instance_id. \"port_forwarding\" carries port forwarding rules and takes no instance_id. Changing it replaces the address.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(client.PublicIPStaticNAT),
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.OneOf(client.PublicIPStaticNAT, client.PublicIPPortForwarding)},
			},
			"instance_id": schema.StringAttribute{
				Description:   "Id of the instance a static_nat address maps to. It must be in network_id. Not allowed for port_forwarding. Changing it replaces the address.",
				Optional:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.IDPrefix("instance", "vm_")},
			},
			"address": schema.StringAttribute{
				Description:   "The public IPv4 address.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"region": schema.StringAttribute{
				Description:   "Region the address lives in.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"zone": schema.StringAttribute{
				Description:   "Zone the address lives in.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the address as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the address was allocated, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"updated_at": schema.StringAttribute{
				Description: "When the address was last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

// ValidateConfig enforces what the purpose implies for instance_id. A purpose or
// instance_id that is still unknown is skipped, because it is not decided yet.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Purpose.IsUnknown() || cfg.InstanceID.IsUnknown() {
		return
	}
	purpose := cfg.Purpose.ValueString()
	if cfg.Purpose.IsNull() {
		purpose = client.PublicIPStaticNAT // the schema default
	}
	switch {
	case purpose == client.PublicIPStaticNAT && cfg.InstanceID.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "Missing instance_id",
			"A static_nat public IP maps to one instance, so instance_id is required. For an address that carries port forwarding rules, set purpose = \"port_forwarding\".")
	case purpose == client.PublicIPPortForwarding && !cfg.InstanceID.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "instance_id not allowed",
			"A port_forwarding public IP names its instances in each pantechdynamics_port_forwarding_rule, so instance_id must be omitted.")
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(publicIPAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create allocates the address, saves its id straight away, then waits for it to
// become active.
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

	ref, err := r.api.CreatePublicIP(ctx, toCreateRequest(plan))
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error creating public IP", err, attributeFor)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, ref.ResourceID))...)

	done := resourcekit.IsActive(r.status(ref.ResourceID), r.api.GetOperation, "public IP", ref.ResourceID, ref.OperationID)
	if err := r.api.WaitForOperation(ctx, ref.OperationID, done); err != nil {
		addCreateWaitError(&resp.Diagnostics, ref.ResourceID, err)
		return
	}
	ip, err := r.api.GetPublicIP(ctx, ref.ResourceID)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading public IP after the change", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, ip))...)
}

// Read refreshes state. An address released outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ip, err := r.api.GetPublicIP(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && ip.ObservedState == resourcekit.StateDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading public IP", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, ip))...)
}

// Update only runs when the timeouts block changed, because every other argument
// replaces the address. It stores the new block and leaves the API alone.
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

// Delete releases the address and waits until it is gone. An address that is
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
	ref, err := r.api.DeletePublicIP(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting public IP", err, map[string]string{
			client.CodeInvalidResourceState: "A public IP cannot be released while port forwarding rules still use it. Remove those rules first, then try again.",
		})
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsGone(r.status(id))); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting public IP", "public IP", id, err)
	}
}

// ImportState adopts an existing public IP by id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, "public IP", idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// status reads the address's state for the shared done checks.
func (r *Resource) status(id string) resourcekit.StatusGetter {
	return func(ctx context.Context) (resourcekit.Status, error) {
		ip, err := r.api.GetPublicIP(ctx, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		return resourcekit.Status{Observed: ip.ObservedState, Desired: ip.DesiredState}, nil
	}
}

// addCreateWaitError adds a hint when the region ran out of addresses.
func addCreateWaitError(diags *diag.Diagnostics, id string, err error) {
	var opErr *client.OperationError
	if errors.As(err, &opErr) && opErr.Operation.Failure != nil && opErr.Operation.Failure.Code == codeIPPoolExhausted {
		diags.AddError("Error creating public IP", err.Error()+fmt.Sprintf("\n\nThe region has no free public addresses right now. Try again later. The failed address %s was saved to state, so the next apply replaces it.", id))
		return
	}
	resourcekit.AddWaitError(diags, "Error creating public IP", "public IP", id, err)
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "network_id", "purpose", "instance_id":
		return path.Root(field), true
	}
	return path.Path{}, false
}
