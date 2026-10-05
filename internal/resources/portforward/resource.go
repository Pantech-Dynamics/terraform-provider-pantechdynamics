// Package portforward implements the pantechdynamics_port_forwarding_rule resource.
package portforward

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix         = "pfr_"
	publicIPIDPrefix = "pip_"
)

// ruleAPI is the part of the API client this resource needs. It is defined here,
// by the consumer, so tests can fake it.
type ruleAPI interface {
	CreatePortForwardingRule(ctx context.Context, publicIPID string, req client.CreatePortForwardingRuleRequest) (*client.OperationReference, error)
	GetPortForwardingRule(ctx context.Context, publicIPID, id string) (*client.PortForwardingRule, error)
	DeletePortForwardingRule(ctx context.Context, id string) (*client.OperationReference, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one port forwarding rule.
type Resource struct {
	api ruleAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_port_forwarding_rule.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_port_forwarding_rule"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceIfSet := []planmodifier.Int64{int64planmodifier.RequiresReplaceIfConfigured()}
	resp.Schema = schema.Schema{
		Description: "Forwards a public port (or range) of a port_forwarding public IP to the same-width private range on one instance. A forward opens nothing on its own: the instance's subnet firewall must also allow the private port, with a pantechdynamics_firewall_rule. The platform cannot edit a rule, so changing any argument replaces it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the rule, starting with pfr_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"public_ip_id": schema.StringAttribute{
				Description:   "Id of the public IP, which must have purpose port_forwarding. Changing it replaces the rule.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IDPrefix("public IP", publicIPIDPrefix)},
			},
			"instance_id": schema.StringAttribute{
				Description:   "Id of the instance to forward to. It must be in the public IP's network. Changing it replaces the rule.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IDPrefix("instance", "vm_")},
			},
			"protocol": schema.StringAttribute{
				Description:   "\"tcp\" (the default) or \"udp\". Changing it replaces the rule.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("tcp"),
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.OneOf("tcp", "udp")},
			},
			"public_port_start": schema.Int64Attribute{
				Description:   "First public port, 1 to 65535. Ranges may not overlap another rule on the address for the same protocol. Changing it replaces the rule.",
				Required:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				Validators:    []validator.Int64{resourcekit.IntBetween(1, 65535)},
			},
			"public_port_end": schema.Int64Attribute{
				Description:   "Last public port of the range. Defaults to public_port_start, a single port. Changing it replaces the rule.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: replaceIfSet,
				Validators:    []validator.Int64{resourcekit.IntBetween(1, 65535)},
			},
			"private_port_start": schema.Int64Attribute{
				Description:   "First private port on the instance. Defaults to public_port_start. Changing it replaces the rule.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: replaceIfSet,
				Validators:    []validator.Int64{resourcekit.IntBetween(1, 65535)},
			},
			"private_port_end": schema.Int64Attribute{
				Description:   "Last private port. Defaults so the private range is as wide as the public one. Changing it replaces the rule.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: replaceIfSet,
				Validators:    []validator.Int64{resourcekit.IntBetween(1, 65535)},
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the rule as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the rule was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the rule was last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

// ValidateConfig rejects a range that runs backwards. Unknown values are skipped.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	checkRange(&resp.Diagnostics, "public_port_end", cfg.PublicPortStart, cfg.PublicPortEnd)
	checkRange(&resp.Diagnostics, "private_port_end", cfg.PrivatePortStart, cfg.PrivatePortEnd)
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(ruleAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create adds the rule, saves its id straight away, then waits for it to become
// active.
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

	ipID := plan.PublicIPID.ValueString()
	ref, err := r.api.CreatePortForwardingRule(ctx, ipID, toCreateRequest(plan))
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error creating port forwarding rule", err, attributeFor)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, ref.ResourceID))...)

	done := resourcekit.IsActive(r.status(ipID, ref.ResourceID), r.api.GetOperation, "port forwarding rule", ref.ResourceID, ref.OperationID)
	if err := r.api.WaitForOperation(ctx, ref.OperationID, done); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating port forwarding rule", "port forwarding rule", ref.ResourceID, err)
		return
	}
	rule, err := r.api.GetPortForwardingRule(ctx, ipID, ref.ResourceID)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading port forwarding rule after the change", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, rule))...)
}

// Read refreshes state. A rule deleted outside Terraform, or whose address is
// gone, is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rule, err := r.api.GetPortForwardingRule(ctx, state.PublicIPID.ValueString(), state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && rule.ObservedState == resourcekit.StateDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading port forwarding rule", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, rule))...)
}

// Update only runs when the timeouts block changed, because every other argument
// replaces the rule. It stores the new block and leaves the API alone.
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

// Delete removes the rule and waits until it is gone. A rule that is already
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

	id, ipID := state.ID.ValueString(), state.PublicIPID.ValueString()
	ref, err := r.api.DeletePortForwardingRule(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error deleting port forwarding rule", err, nil)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsGone(r.status(ipID, id))); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting port forwarding rule", "port forwarding rule", id, err)
	}
}

// ImportState adopts a rule by "public_ip_id/rule_id". The address is part of the
// id because the API reads a rule through its public IP.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ipID, id, ok := strings.Cut(req.ID, "/")
	if !ok || !strings.HasPrefix(ipID, publicIPIDPrefix) || !strings.HasPrefix(id, idPrefix) {
		resp.Diagnostics.AddError("Invalid port forwarding rule id",
			fmt.Sprintf("Expected \"<public ip id>/<rule id>\", for example pip_abc/pfr_def, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("public_ip_id"), ipID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// status reads the rule's state for the shared done checks.
func (r *Resource) status(publicIPID, id string) resourcekit.StatusGetter {
	return func(ctx context.Context) (resourcekit.Status, error) {
		rule, err := r.api.GetPortForwardingRule(ctx, publicIPID, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		return resourcekit.Status{Observed: rule.ObservedState, Desired: rule.DesiredState}, nil
	}
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "instance_id", "protocol", "public_port_start", "public_port_end", "private_port_start", "private_port_end":
		return path.Root(field), true
	}
	return path.Path{}, false
}

// checkRange adds an error if both ends are known and the end is below the start.
func checkRange(diags interface {
	AddAttributeError(path.Path, string, string)
}, endName string, start, end types.Int64) {
	if start.IsNull() || start.IsUnknown() || end.IsNull() || end.IsUnknown() {
		return
	}
	if end.ValueInt64() < start.ValueInt64() {
		diags.AddAttributeError(path.Root(endName), "Invalid port range",
			fmt.Sprintf("%s (%d) must not be lower than the first port (%d).", endName, end.ValueInt64(), start.ValueInt64()))
	}
}
