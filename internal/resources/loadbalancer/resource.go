// Package loadbalancer implements the pantechdynamics_load_balancer resource.
package loadbalancer

import (
	"context"
	"errors"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "lb_"
	kind     = "load balancer"
)

// stateSyncing is reported to the shared done check while an active load
// balancer has not yet applied every change. It is never stored.
const stateSyncing = "syncing"

// lbAPI is the part of the API client this resource needs. It is defined here,
// by the consumer, so tests can fake it.
type lbAPI interface {
	CreateLoadBalancer(ctx context.Context, req client.CreateLoadBalancerRequest) (*client.OperationReference, error)
	GetLoadBalancer(ctx context.Context, id string) (*client.LoadBalancer, error)
	UpdateLoadBalancer(ctx context.Context, id string, req client.UpdateLoadBalancerRequest) (*client.OperationReference, error)
	DeleteLoadBalancer(ctx context.Context, id string) (*client.OperationReference, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one load balancer.
type Resource struct {
	api lbAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_load_balancer.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	emptySet := types.SetValueMust(types.StringType, nil)
	resp.Schema = schema.Schema{
		Description: "Spreads one TCP port of a load_balancer public IP across instances in one subnet of a VPC network. One public IP can carry several load balancers, one per public port. A load balancer opens nothing on its own: the subnet's firewall must also allow private_port, with a pantechdynamics_firewall_rule. The name, algorithm and targets change in place; changing the public IP, subnet, ports or cidr_list replaces the load balancer.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the load balancer, starting with lb_.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"name": schema.StringAttribute{
				Description: "Name of the load balancer, 1 to 63 characters. Changes in place.",
				Required:    true,
				Validators:  []validator.String{resourcekit.NameLength("load balancer", maxNameLen)},
			},
			"public_ip_id": schema.StringAttribute{
				Description:   "Id of the public IP clients connect to. It must be active and have purpose load_balancer. Changing it replaces the load balancer.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IDPrefix("public IP", "pip_")},
			},
			"subnet_id": schema.StringAttribute{
				Description:   "Id of a subnet in the public IP's network. Every target must be in it. Changing it replaces the load balancer.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IDPrefix("subnet", "snet_")},
			},
			"algorithm": schema.StringAttribute{
				Description: "How connections are spread: \"roundrobin\" (the default) takes turns, \"leastconn\" picks the target with the fewest connections, \"source\" keeps a client on one target. Changes in place.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("roundrobin"),
				Validators:  []validator.String{resourcekit.OneOf("roundrobin", "leastconn", "source")},
			},
			"public_port": schema.Int64Attribute{
				Description:   "TCP port clients connect to, 1 to 65535. Only one load balancer per public IP may use a port. Changing it replaces the load balancer.",
				Required:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				Validators:    []validator.Int64{resourcekit.IntBetween(1, 65535)},
			},
			"private_port": schema.Int64Attribute{
				Description:   "Port on each target, 1 to 65535. Defaults to public_port. Changing it replaces the load balancer.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{privatePortDefault{}, int64planmodifier.RequiresReplace()},
				Validators:    []validator.Int64{resourcekit.IntBetween(1, 65535)},
			},
			"cidr_list": schema.SetAttribute{
				Description:   "Source addresses allowed to connect, as at most 20 IPv4 CIDRs. Empty (the default) allows any source. Changing it replaces the load balancer.",
				ElementType:   types.StringType,
				Optional:      true,
				Computed:      true,
				Default:       setdefault.StaticValue(emptySet),
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
				Validators:    []validator.Set{cidrListValidator{}},
			},
			"instance_ids": schema.SetAttribute{
				Description: "Ids of the instances to spread traffic across, at most 50, each in subnet_id. Empty (the default) means no targets yet. Changes in place: the set is the whole list of targets. An instance cannot be deleted while it is a target.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     setdefault.StaticValue(emptySet),
				Validators:  []validator.Set{instanceIDsValidator{}},
			},
			"public_ip_address": schema.StringAttribute{
				Description:   "The public IPv4 address clients connect to.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"network_id": schema.StringAttribute{
				Description:   "Id of the VPC network the load balancer is in.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"protocol": schema.StringAttribute{
				Description:   "Protocol balanced. Always \"tcp\".",
				Computed:      true,
				PlanModifiers: keep,
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the load balancer as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the load balancer was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"updated_at": schema.StringAttribute{
				Description: "When the load balancer was last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(lbAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create adds the load balancer, saves its id straight away, then waits until
// it is active with every target in place.
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

	body, diags := toCreateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref, err := r.api.CreateLoadBalancer(ctx, body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating load balancer", err, attributeFor, createHints)
		return
	}
	id := ref.ResourceID
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, id))...)

	if err := r.wait(ctx, ref.OperationID, id, body.InstanceIDs); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating load balancer", kind, id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// Read refreshes state. A load balancer deleted outside Terraform is removed
// from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lb, err := r.api.GetLoadBalancer(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && lb.ObservedState == resourcekit.StateDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading load balancer", err, nil)
		return
	}
	m, diags := fromAPIResponse(ctx, state, lb)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Update renames the load balancer, changes its algorithm or replaces its
// targets with one PATCH, then waits until the change is applied. A plan that
// only changes the timeouts block sends nothing.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, plan.Timeouts.Update, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	body, changed, diags := toUpdateRequest(ctx, state, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if !changed {
		state.Timeouts = plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}

	ref, err := r.api.UpdateLoadBalancer(ctx, id, body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating load balancer", err, attributeFor, updateHints)
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...) // nothing changed
		return
	}
	if ref.OperationID != "" { // empty means the backend had nothing to change
		wanted, d := stringsOf(ctx, plan.InstanceIDs)
		resp.Diagnostics.Append(d...)
		if err := r.wait(ctx, ref.OperationID, id, wanted); err != nil {
			resourcekit.AddWaitError(&resp.Diagnostics, "Error updating load balancer", kind, id, err)
			r.refreshOrKeep(ctx, state, id, &resp.State, &resp.Diagnostics)
			return
		}
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...) // the next refresh catches up
	}
}

// Delete removes the load balancer and waits until it is gone. One that is
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
	ref, err := r.api.DeleteLoadBalancer(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error deleting load balancer", err, nil)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsGone(r.rawStatus(id))); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting load balancer", kind, id, err)
	}
}

// ImportState adopts an existing load balancer by id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// wait follows a create or update until the load balancer is active, in sync
// and serving exactly the wanted targets, or the operation ends.
func (r *Resource) wait(ctx context.Context, opID, id string, wanted []string) error {
	done := resourcekit.IsActive(r.settledStatus(id, wanted), r.api.GetOperation, kind, id, opID)
	return r.api.WaitForOperation(ctx, opID, done)
}

// refresh reads the load balancer and stores it, keeping what only Terraform
// knows from prev.
func (r *Resource) refresh(ctx context.Context, prev model, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	lb, err := r.api.GetLoadBalancer(ctx, id)
	if err != nil {
		resourcekit.AddAPIError(diags, "Error reading load balancer after the change", err, nil)
		return
	}
	m, d := fromAPIResponse(ctx, prev, lb)
	diags.Append(d...)
	diags.Append(st.Set(ctx, m)...)
}

// refreshOrKeep stores what the platform reports after a failed change, or the
// previous state when it cannot be read, so state never holds planned values
// that were not applied.
func (r *Resource) refreshOrKeep(ctx context.Context, prev model, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	lb, err := r.api.GetLoadBalancer(ctx, id)
	if err != nil {
		diags.Append(st.Set(ctx, prev)...)
		return
	}
	m, d := fromAPIResponse(ctx, prev, lb)
	diags.Append(d...)
	diags.Append(st.Set(ctx, m)...)
}

// settledStatus reports "active" only once the load balancer is active, in sync
// and its targets are exactly wanted, so the shared done check waits for all
// three. A failed load balancer is reported as failed.
func (r *Resource) settledStatus(id string, wanted []string) resourcekit.StatusGetter {
	want := slices.Clone(wanted)
	slices.Sort(want)
	return func(ctx context.Context) (resourcekit.Status, error) {
		lb, err := r.api.GetLoadBalancer(ctx, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		st := resourcekit.Status{Observed: lb.ObservedState, Desired: lb.DesiredState}
		if st.Observed == resourcekit.StateActive && (!lb.InSync || !slices.Equal(targetIDs(lb), nonNil(want)) || removing(lb)) {
			st.Observed = stateSyncing
		}
		return st, nil
	}
}

// rawStatus reads the load balancer's state for the delete check.
func (r *Resource) rawStatus(id string) resourcekit.StatusGetter {
	return func(ctx context.Context) (resourcekit.Status, error) {
		lb, err := r.api.GetLoadBalancer(ctx, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		return resourcekit.Status{Observed: lb.ObservedState, Desired: lb.DesiredState}, nil
	}
}

// removing reports whether a target is still being taken out.
func removing(lb *client.LoadBalancer) bool {
	for _, m := range lb.Members {
		if m.DesiredState == memberRemoved {
			return true
		}
	}
	return false
}

// createHints explain the create refusals a user can act on.
var createHints = map[string]string{
	client.CodePublicIPNotForLoadBalancer: "Use an active pantechdynamics_public_ip with purpose = \"load_balancer\" in the subnet's network.",
	client.CodePortInUse:                  "Another load balancer on this public IP already uses public_port. Choose another port, or delete that load balancer first.",
}

// updateHints explain the update refusals a user can act on.
var updateHints = map[string]string{
	client.CodeLoadBalancerNotChangeable: "The load balancer is being deleted or has failed, so it cannot be changed. Replace it with `terraform apply -replace`.",
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "public_ip_id", "subnet_id", "algorithm", "public_port", "private_port", "cidr_list", "instance_ids":
		return path.Root(field), true
	}
	return path.Path{}, false
}
