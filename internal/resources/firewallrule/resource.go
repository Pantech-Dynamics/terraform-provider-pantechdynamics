// Package firewallrule implements the pantechdynamics_firewall_rule resource.
package firewallrule

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix       = "aclr_"
	subnetIDPrefix = "snet_"

	// fieldCodeRuleNumberTaken is the field error on number. The top-level code is
	// the generic VALIDATION_FAILED, so it is read from the errors list.
	fieldCodeRuleNumberTaken = "RULE_NUMBER_TAKEN"

	// A deleted rule keeps its number for a few seconds (3 to 5 on staging) after
	// it disappears from the API, so replacing a rule, which deletes it and adds
	// it again under the same number, is refused at first. The create is retried
	// when no other rule holds the number.
	numberRetryDelay = 3 * time.Second
	numberRetryMax   = 10

	codeRulesNotSupported   = "FIREWALL_RULES_NOT_SUPPORTED"
	codeSubnetNotUsable     = "SUBNET_NOT_USABLE"
	codeInvalidFirewallRule = "INVALID_FIREWALL_RULE"
)

// ruleAPI is the part of the API client this resource needs. It is defined here,
// by the consumer, so tests can fake it.
type ruleAPI interface {
	CreateFirewallRule(ctx context.Context, subnetID string, req client.CreateFirewallRuleRequest) (*client.OperationReference, error)
	ListFirewallRules(ctx context.Context, subnetID string) ([]client.FirewallRule, error)
	GetFirewallRule(ctx context.Context, subnetID, id string) (*client.FirewallRule, error)
	DeleteFirewallRule(ctx context.Context, id string) (*client.OperationReference, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one firewall rule on a VPC subnet.
type Resource struct {
	api ruleAPI

	// numberRetryDelay is the pause between retries of a number the platform still
	// holds. Zero means the default, so tests can shorten it.
	numberRetryDelay time.Duration
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_firewall_rule.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_rule"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceInt := []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A firewall rule on a VPC subnet. Rules are stateless, so return traffic needs its own rule, and are evaluated by number, lowest first. The platform cannot edit a rule, so changing any argument replaces it. Not for standard instances: use pantechdynamics_security_group for those.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the rule, starting with aclr_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"subnet_id": schema.StringAttribute{
				Description:   "Id of the subnet the rule applies to. Changing it replaces the rule.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IDPrefix("subnet", subnetIDPrefix)},
			},
			"number": schema.Int64Attribute{
				Description:   "Evaluation order, 100 to 9999, lowest first. Unique on the subnet. Changing it replaces the rule.",
				Required:      true,
				PlanModifiers: replaceInt,
				Validators:    []validator.Int64{resourcekit.IntBetween(100, 9999)},
			},
			"direction": schema.StringAttribute{
				Description:   "\"ingress\" (traffic into the subnet, the default) or \"egress\" (traffic out of it). Changing it replaces the rule.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("ingress"),
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.OneOf("ingress", "egress")},
			},
			"protocol": schema.StringAttribute{
				Description:   "\"tcp\", \"udp\", \"icmp\" or \"all\". Changing it replaces the rule.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.OneOf(protoTCP, protoUDP, protoICMP, "all")},
			},
			"port_start": schema.Int64Attribute{
				Description:   "First port, 1 to 65535. Required for tcp and udp, and not allowed for icmp and all. Changing it replaces the rule.",
				Optional:      true,
				PlanModifiers: replaceInt,
				Validators:    []validator.Int64{resourcekit.IntBetween(1, 65535)},
			},
			"port_end": schema.Int64Attribute{
				Description:   "Last port of the range, 1 to 65535. Defaults to port_start, which means a single port. Changing it replaces the rule.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIfConfigured()},
				Validators:    []validator.Int64{resourcekit.IntBetween(1, 65535)},
			},
			"icmp_type": schema.Int64Attribute{
				Description:   "ICMP type, 0 to 255, for the icmp protocol only. Omit it to match any type. Changing it replaces the rule.",
				Optional:      true,
				PlanModifiers: replaceInt,
				Validators:    []validator.Int64{resourcekit.IntBetween(0, 255)},
			},
			"icmp_code": schema.Int64Attribute{
				Description:   "ICMP code, 0 to 255, for the icmp protocol only. Omit it to match any code. Changing it replaces the rule.",
				Optional:      true,
				PlanModifiers: replaceInt,
				Validators:    []validator.Int64{resourcekit.IntBetween(0, 255)},
			},
			"cidr": schema.StringAttribute{
				Description:   "IPv4 CIDR the rule matches: the source for ingress, the destination for egress. Use \"0.0.0.0/0\" for anywhere. Changing it replaces the rule.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IPv4CIDR()},
			},
			"action": schema.StringAttribute{
				Description:   "\"allow\" (the default) or \"deny\". Changing it replaces the rule.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("allow"),
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.OneOf("allow", "deny")},
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

	subnetID := plan.SubnetID.ValueString()
	ref, err := r.createRule(ctx, subnetID, toCreateRequest(plan))
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating firewall rule", err, attributeFor, createHints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, ref.ResourceID))...)

	done := resourcekit.IsActive(r.status(subnetID, ref.ResourceID), r.api.GetOperation, "firewall rule", ref.ResourceID, ref.OperationID)
	if err := r.api.WaitForOperation(ctx, ref.OperationID, done); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating firewall rule", "firewall rule", ref.ResourceID, err)
		return
	}
	rule, err := r.api.GetFirewallRule(ctx, subnetID, ref.ResourceID)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading firewall rule after the change", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, rule))...)
}

// Read refreshes state. A rule deleted outside Terraform, or whose subnet is
// gone, is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rule, err := r.api.GetFirewallRule(ctx, state.SubnetID.ValueString(), state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && rule.ObservedState == resourcekit.StateDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading firewall rule", err, nil)
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

	id, subnetID := state.ID.ValueString(), state.SubnetID.ValueString()
	ref, err := r.api.DeleteFirewallRule(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error deleting firewall rule", err, nil)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, resourcekit.IsGone(r.status(subnetID, id))); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting firewall rule", "firewall rule", id, err)
	}
}

// ImportState adopts a rule by "subnet_id/rule_id". The subnet is part of the id
// because the API reads a rule through its subnet.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	subnetID, id, ok := strings.Cut(req.ID, "/")
	if !ok || !strings.HasPrefix(subnetID, subnetIDPrefix) || !strings.HasPrefix(id, idPrefix) {
		resp.Diagnostics.AddError("Invalid firewall rule id",
			fmt.Sprintf("Expected \"<subnet id>/<rule id>\", for example snet_abc/aclr_def, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("subnet_id"), subnetID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// createRule adds the rule. A refused number is retried only when no live rule on
// the subnet holds it: then the number is the leftover of a rule that was just
// deleted, and it frees within seconds. If another rule holds it, the refusal is
// real and is returned at once.
func (r *Resource) createRule(ctx context.Context, subnetID string, req client.CreateFirewallRuleRequest) (*client.OperationReference, error) {
	delay := r.numberRetryDelay
	if delay == 0 {
		delay = numberRetryDelay
	}
	for attempt := 1; ; attempt++ {
		ref, err := r.api.CreateFirewallRule(ctx, subnetID, req)
		if err == nil || !client.HasFieldCode(err, "number", fieldCodeRuleNumberTaken) || attempt >= numberRetryMax {
			return ref, err
		}
		taken, listErr := r.numberInUse(ctx, subnetID, req.Number)
		if listErr != nil || taken {
			return ref, err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, err
		}
	}
}

// numberInUse reports whether a live rule on the subnet has this number.
func (r *Resource) numberInUse(ctx context.Context, subnetID string, number int64) (bool, error) {
	rules, err := r.api.ListFirewallRules(ctx, subnetID)
	if err != nil {
		return false, err
	}
	for _, rule := range rules {
		if rule.Number == number && rule.ObservedState != resourcekit.StateDeleted {
			return true, nil
		}
	}
	return false, nil
}

// status reads the rule's state for the shared done checks.
func (r *Resource) status(subnetID, id string) resourcekit.StatusGetter {
	return func(ctx context.Context) (resourcekit.Status, error) {
		rule, err := r.api.GetFirewallRule(ctx, subnetID, id)
		if err != nil {
			return resourcekit.Status{}, err
		}
		return resourcekit.Status{Observed: rule.ObservedState, Desired: rule.DesiredState}, nil
	}
}

// createHints explain the refusals a user can act on.
var createHints = map[string]string{
	codeRulesNotSupported:   "Firewall rules exist only on VPC subnets. For a standard instance use pantechdynamics_security_group instead.",
	codeSubnetNotUsable:     "The subnet is not active yet, or is being deleted. Make sure it exists and is active, then try again.",
	codeInvalidFirewallRule: "The platform refused this combination of protocol, ports and CIDR.",
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "number", "direction", "protocol", "port_start", "port_end", "icmp_type", "icmp_code", "cidr", "action":
		return path.Root(field), true
	}
	return path.Path{}, false
}
