// Package securitygroup implements the pantechdynamics_security_group resource.
package securitygroup

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

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

const (
	// idPrefix is the prefix every security group id carries.
	idPrefix = "sg_"

	// defaultTimeout bounds each create, update and delete wait. The API usually
	// finishes in seconds, but dev calls have taken minutes.
	defaultTimeout = 10 * time.Minute

	stateActive  = "active"
	stateFailed  = "failed"
	stateDeleted = "deleted"
)

// groupAPI is the part of the API client this resource needs. It is defined here,
// by the consumer, so tests can fake it.
type groupAPI interface {
	CreateSecurityGroup(ctx context.Context, req client.CreateSecurityGroupRequest) (*client.OperationReference, error)
	GetSecurityGroup(ctx context.Context, id string) (*client.SecurityGroup, error)
	ListSecurityGroups(ctx context.Context) ([]client.SecurityGroup, error)
	ReplaceSecurityGroupRules(ctx context.Context, id string, rules []client.SecurityGroupRule) (*client.OperationReference, error)
	DeleteSecurityGroup(ctx context.Context, id string) (*client.OperationReference, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one security group.
type Resource struct {
	api groupAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_security_group.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A security group: a reusable firewall for standard instances. Inbound traffic that no rule allows is dropped. Changing the rules updates the group in place and affects every instance that uses it. Renaming it replaces it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the group, starting with sg_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Name of the group, 1 to 255 characters and unique on the account. Changing it replaces the group.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{nameValidator{}},
			},
			"rules": schema.SetNestedAttribute{
				Description: "The firewall rules. At least one is required. The set is replaced as a whole, so a rule that is not listed is removed, and the order does not matter.",
				Required:    true,
				Validators:  []validator.Set{rulesValidator{}},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"direction": schema.StringAttribute{Required: true, Description: "\"ingress\" (traffic into the instance) or \"egress\" (traffic out of it)."},
						"protocol":  schema.StringAttribute{Required: true, Description: "\"tcp\", \"udp\", \"icmp\" or \"all\"."},
						"port_range": schema.StringAttribute{
							Optional:    true,
							Description: "A single port (\"22\") or an inclusive range (\"8000-8080\"). Omit it to mean all ports. It must be omitted for the icmp and all protocols.",
						},
						"cidr": schema.StringAttribute{Required: true, Description: "IPv4 CIDR the rule applies to, for example \"10.0.0.0/8\". Use \"0.0.0.0/0\" for anywhere."},
					},
				},
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the group as the platform sees it, for example \"active\".",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the group was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the group was last changed, in RFC 3339 UTC, to the second.",
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
	api, ok := req.ProviderData.(groupAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	r.api = api
}

// Create asks the backend to create the group, saves its id straight away, then
// waits. Saving first means a timeout or a cancel cannot orphan a group that the
// backend goes on to create.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Create, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	rules, diags := rulesFromSet(ctx, plan.Rules)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The backend answers a duplicate name with a slow, opaque 500, so look first.
	if err := r.ensureNameFree(ctx, plan.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Security group name already in use", err.Error())
		return
	}

	ref, err := r.api.CreateSecurityGroup(ctx, client.CreateSecurityGroupRequest{Name: plan.Name.ValueString(), Rules: rules})
	if err != nil {
		addCreateError(&resp.Diagnostics, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, ref.ResourceID))...)

	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.isActive(ref.ResourceID)); err != nil {
		addWaitError(&resp.Diagnostics, "Error creating security group", ref.ResourceID, err)
		return
	}
	r.refresh(ctx, plan, ref.ResourceID, &resp.State, &resp.Diagnostics)
}

// Read refreshes state from the API. A group deleted outside Terraform is removed
// from state so the next plan recreates it.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sg, err := r.api.GetSecurityGroup(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && sg.ObservedState == stateDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error reading security group", err)
		return
	}

	next, diags := fromAPIResponse(ctx, state, sg)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

// Update replaces the group's rule set in place. A rename never reaches here: it
// replaces the group.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Update, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	rules, diags := rulesFromSet(ctx, plan.Rules)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Only the timeouts block changed: nothing to send to the API.
	if plan.Rules.Equal(state.Rules) {
		r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
		return
	}

	ref, err := r.api.ReplaceSecurityGroupRules(ctx, id, rules)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error updating security group rules", err)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.hasRules(id, rules)); err != nil {
		addWaitError(&resp.Diagnostics, "Error updating security group rules", id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// Delete removes the group and waits until it is gone. A group that is already
// gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, state.Timeouts.Delete, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	ref, err := r.api.DeleteSecurityGroup(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		addDeleteError(&resp.Diagnostics, err)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.isGone(id)); err != nil {
		addWaitError(&resp.Diagnostics, "Error deleting security group", id, err)
	}
}

// ImportState adopts an existing group by id. A wrong id fails here with a clear
// message instead of a 404 afterwards.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !strings.HasPrefix(req.ID, idPrefix) {
		resp.Diagnostics.AddError("Invalid security group id", fmt.Sprintf("Expected an id starting with %q, got %q.", idPrefix, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// refresh reads the group and writes it to state. After an accepted write the
// group must exist, so a 404 here is an error, not a removal.
func (r *Resource) refresh(ctx context.Context, prev model, id string, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	sg, err := r.api.GetSecurityGroup(ctx, id)
	if err != nil {
		addAPIError(diags, "Error reading security group after the change", err)
		return
	}
	next, d := fromAPIResponse(ctx, prev, sg)
	diags.Append(d...)
	diags.Append(state.Set(ctx, next)...)
}

// ensureNameFree fails if a group with this name already exists.
func (r *Resource) ensureNameFree(ctx context.Context, name string) error {
	groups, err := r.api.ListSecurityGroups(ctx)
	if err != nil {
		return fmt.Errorf("checking existing security groups: %w", err)
	}
	for _, g := range groups {
		if g.Name == name {
			return fmt.Errorf("a security group named %q already exists (%s). Choose another name, or bring it under Terraform with `terraform import pantechdynamics_security_group.<name> %s`", name, g.ID, g.ID)
		}
	}
	return nil
}

// isActive finishes a create when the group is active. The operation record is
// not relied on alone, because the resource is the authority.
func (r *Resource) isActive(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		sg, err := r.api.GetSecurityGroup(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		if sg.ObservedState == stateFailed {
			return false, fmt.Errorf("security group %s entered the %q state", id, stateFailed)
		}
		return sg.ObservedState == stateActive, nil
	}
}

// hasRules finishes an update when the group is active and holds the new rules.
func (r *Resource) hasRules(id string, want []client.SecurityGroupRule) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		sg, err := r.api.GetSecurityGroup(ctx, id)
		if err != nil {
			return false, err
		}
		return sg.ObservedState == stateActive && sameRules(sg.Rules, want), nil
	}
}

// isGone finishes a delete when the group no longer exists. This matters because
// the delete operation can stay "submitted" long after the group is gone.
func (r *Resource) isGone(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		sg, err := r.api.GetSecurityGroup(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return sg.ObservedState == stateDeleted, nil
	}
}

// withTimeout applies the configured timeout, or the default, to ctx.
func withTimeout(ctx context.Context, get func(context.Context, time.Duration) (time.Duration, diag.Diagnostics), diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, defaultTimeout)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}
