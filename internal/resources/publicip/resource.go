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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "pip_"

	// codeIPPoolExhausted is the operation failure when the region ran out of
	// addresses between the availability check and the request.
	codeIPPoolExhausted = "IP_POOL_EXHAUSTED"

	// stateSyncing stands in for "active" while an attach or detach has not
	// reached the address yet (in_sync false), or it does not point where it
	// should yet, so the shared done check keeps waiting.
	stateSyncing = "syncing"
)

// publicIPAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type publicIPAPI interface {
	CreatePublicIP(ctx context.Context, req client.CreatePublicIPRequest) (*client.OperationReference, error)
	GetPublicIP(ctx context.Context, id string) (*client.PublicIP, error)
	DeletePublicIP(ctx context.Context, id string) (*client.OperationReference, error)
	AttachPublicIP(ctx context.Context, id, instanceID string) (*client.OperationReference, error)
	DetachPublicIP(ctx context.Context, id string) (*client.OperationReference, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
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
		Description: "A public IPv4 address on a VPC network, billed monthly while it is held. A static_nat address maps every port to one instance; it can be reserved without one, and instance_id can be changed or removed in place, which attaches, moves or detaches the address without changing it. A detached address is still held and still billed, once. A port_forwarding address carries pantechdynamics_port_forwarding_rule resources. A load_balancer address carries pantechdynamics_load_balancer resources, one per public port. The address opens nothing on its own: the subnet's firewall rules must allow the traffic. Changing network_id or purpose replaces it and the address changes. An organization can hold 20 public IPs of any purpose by default; past that a create is refused with PUBLIC_IP_LIMIT_EXCEEDED.",
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
				Description:   "\"static_nat\" (the default) maps every port of the address to the instance in instance_id, or holds the address unattached when instance_id is omitted. \"port_forwarding\" carries port forwarding rules and takes no instance_id. \"load_balancer\" carries load balancers and takes no instance_id. Changing it replaces the address.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(client.PublicIPStaticNAT),
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.OneOf(client.PublicIPStaticNAT, client.PublicIPPortForwarding, client.PublicIPLoadBalancer)},
			},
			"instance_id": schema.StringAttribute{
				Description: "Id of the instance a static_nat address maps to. It must be in network_id and must not already have a static_nat address of its own. Omit it to reserve the address unattached. Changing it moves the address to the new instance, and removing it detaches the address, both in place: the address and its id stay the same, and it stays billed while held. Not allowed for port_forwarding or load_balancer.",
				Optional:    true,
				Validators:  []validator.String{resourcekit.IDPrefix("instance", "vm_")},
			},
			"in_sync": schema.BoolAttribute{
				Description: "False while an attach or detach has not reached the address yet. Terraform waits for it to be true after changing instance_id.",
				Computed:    true,
			},
			"network_name": schema.StringAttribute{
				Description:   "Name of the VPC network the address is for, as the platform reports it.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"instance_name": schema.StringAttribute{
				Description: "Name of the instance a static_nat address maps to, as the platform reports it. Null for port_forwarding and load_balancer.",
				Computed:    true,
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
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
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
	// A static_nat address takes an instance_id or none: without one it is
	// reserved, unattached.
	switch {
	case purpose == client.PublicIPPortForwarding && !cfg.InstanceID.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "instance_id not allowed",
			"A port_forwarding public IP names its instances in each pantechdynamics_port_forwarding_rule, so instance_id must be omitted.")
	case purpose == client.PublicIPLoadBalancer && !cfg.InstanceID.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "instance_id not allowed",
			"A load_balancer public IP names its instances in each pantechdynamics_load_balancer, so instance_id must be omitted.")
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
		addCreateError(&resp.Diagnostics, err)
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

// Update attaches, moves or detaches a static_nat address when instance_id
// changed; every other argument replaces the address. It waits for the
// operation, then for the address to be in sync and point at the wanted
// instance. A change of the timeouts block alone leaves the API untouched.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.InstanceID.Equal(state.InstanceID) {
		state.Timeouts = plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, plan.Timeouts.Update, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	want := plan.InstanceID.ValueString() // "" detaches
	summary := "Error attaching public IP"
	if want == "" {
		summary = "Error detaching public IP"
	}
	ref, err := r.move(ctx, id, want)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, summary, err, attributeFor, moveHints)
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...) // nothing changed
		return
	}
	if err := r.waitSettled(ctx, ref.OperationID, id, want); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, summary, "public IP", id, err)
		r.refreshOrKeep(ctx, state, id, &resp.State, &resp.Diagnostics)
		return
	}
	ip, err := r.api.GetPublicIP(ctx, id)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading public IP after the change", err, nil)
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...) // the next refresh catches up
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, ip))...)
}

// move attaches the address to instanceID, or detaches it when instanceID is
// empty.
func (r *Resource) move(ctx context.Context, id, instanceID string) (*client.OperationReference, error) {
	if instanceID == "" {
		return r.api.DetachPublicIP(ctx, id)
	}
	return r.api.AttachPublicIP(ctx, id, instanceID)
}

// waitSettled follows an attach or detach: first its operation, when there is
// one (an empty id means the backend had nothing to change), then the address
// itself until it is in sync and points at want. The operation can report
// success a moment before the address shows the change.
func (r *Resource) waitSettled(ctx context.Context, opID, id, want string) error {
	done := resourcekit.IsActive(r.settledStatus(id, want), r.api.GetOperation, "public IP", id, opID)
	if opID != "" {
		if err := r.api.WaitForOperation(ctx, opID, done); err != nil {
			return err
		}
	}
	return r.api.WaitUntil(ctx, "public IP "+id+" to be in sync", done)
}

// settledStatus reports "active" only once the address is active, in sync and
// points at want ("" for detached), so the shared done check waits for all
// three. A failed address is reported as failed.
func (r *Resource) settledStatus(id, want string) resourcekit.StatusGetter {
	return func(ctx context.Context) (resourcekit.Status, error) {
		ip, err := r.api.GetPublicIP(ctx, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		st := resourcekit.Status{Observed: ip.ObservedState, Desired: ip.DesiredState}
		got := ""
		if ip.InstanceID != nil {
			got = *ip.InstanceID
		}
		if st.Observed == resourcekit.StateActive && (!ip.InSync || got != want) {
			st.Observed = stateSyncing
		}
		return st, nil
	}
}

// refreshOrKeep stores what the platform reports after a failed change, or the
// previous state when it cannot be read, so state never holds planned values
// that were not applied.
func (r *Resource) refreshOrKeep(ctx context.Context, prev model, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	ip, err := r.api.GetPublicIP(ctx, id)
	if err != nil {
		diags.Append(st.Set(ctx, prev)...)
		return
	}
	diags.Append(st.Set(ctx, fromAPIResponse(prev, ip))...)
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
			client.CodeInvalidResourceState:     "A public IP cannot be released while port forwarding rules still use it. Remove those rules first, then try again.",
			client.CodePublicIPHasLoadBalancers: "A public IP cannot be released while load balancers still use it. Delete those load balancers first, then try again.",
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

// hintAlreadyHasPublicIP explains INSTANCE_ALREADY_HAS_PUBLIC_IP, on a create
// and on an attach.
const hintAlreadyHasPublicIP = "The instance already has a static_nat public IP of its own. Detach that address (remove instance_id from its pantechdynamics_public_ip) or release it, then run `terraform apply` again. To move an address between instances, change instance_id on the one address instead of creating a second."

// createHints explain the create refusals a user can act on.
var createHints = map[string]string{
	client.CodePublicIPLimitExceeded:      "Your organization has reached its limit of public IPs of any purpose (20 by default). Release an address you no longer need, or contact support to raise the limit, then run `terraform apply` again. A detached static_nat address still counts until it is released.",
	client.CodeStaticNATLimitExceeded:     "Your organization has reached its limit of static_nat public IPs. Release one you no longer need, use purpose = \"port_forwarding\" instead, or contact support to raise the limit.",
	client.CodeInstanceAlreadyHasPublicIP: hintAlreadyHasPublicIP,
}

// moveHints explain the attach and detach refusals a user can act on.
var moveHints = map[string]string{
	client.CodeInstanceAlreadyHasPublicIP: hintAlreadyHasPublicIP,
	client.CodePublicIPNotStaticNAT:       "Only a static_nat address can be attached or detached. A port_forwarding or load_balancer address names its instances in its rules or load balancers, so leave instance_id out.",
	client.CodeInvalidResourceState:       "The public IP is busy with another change, for example still being created. Wait for it to finish, then run `terraform apply` again.",
}

// addCreateError reports a refused create: 422 field errors on their
// attributes, and a hint for the limit refusals.
func addCreateError(diags *diag.Diagnostics, err error) {
	resourcekit.AddHintedAPIError(diags, "Error creating public IP", err, attributeFor, createHints)
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
