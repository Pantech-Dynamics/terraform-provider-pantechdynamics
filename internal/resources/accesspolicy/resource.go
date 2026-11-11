// Package accesspolicy implements the pantechdynamics_access_policy resource:
// one of the organization's own access policies. Pantech-managed policies
// (pol_pantech_*) cannot be managed; they are read with the
// pantechdynamics_access_policy data source and attached as they are.
package accesspolicy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/accesspolicy"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "pol_"
	kind     = "access policy"
)

// policyAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type policyAPI interface {
	CreateIAMPolicy(ctx context.Context, req client.IAMPolicyRequest) (*client.IAMPolicy, error)
	GetIAMPolicy(ctx context.Context, id string) (*client.IAMPolicy, error)
	UpdateIAMPolicy(ctx context.Context, id string, version int64, req client.IAMPolicyRequest) (*client.IAMPolicy, error)
	DeleteIAMPolicy(ctx context.Context, id string) error
	ListIAMActions(ctx context.Context) ([]client.IAMAction, error)
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
	_ resource.ResourceWithModifyPlan     = &Resource{}
)

// Resource manages one access policy.
type Resource struct {
	api policyAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// model maps the resource's attributes to Go. Statements are the statement
// blocks; with policy_json instead, there are none.
type model struct {
	ID              types.String                  `tfsdk:"id"`
	Name            types.String                  `tfsdk:"name"`
	Description     types.String                  `tfsdk:"description"`
	Statements      []accesspolicy.StatementModel `tfsdk:"statement"`
	PolicyJSON      types.String                  `tfsdk:"policy_json"`
	AttachmentCount types.Int64                   `tfsdk:"attachment_count"`
	Version         types.Int64                   `tfsdk:"version"`
	CreatedAt       types.String                  `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_access_policy.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_policy"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An access policy: statements that allow or deny actions on resources, attached to members or teams with pantechdynamics_access_policy_attachment. " +
			"Write the statements as statement blocks, or as policy_json (for example from pantechdynamics_access_policy_document); set exactly one. " +
			"Statements are compared by meaning, so reordering actions or writing a CIDR another way does not show a change. " +
			"Pantech's managed policies (pol_pantech_*) cannot be managed here: read them with the pantechdynamics_access_policy data source. At most 100 policies per organization; owners and admins only.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the policy, starting with pol_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description: "A name unique in the organization: 1 to 64 letters, digits, spaces, '.', '_' or '-', starting with a letter or digit. Names starting with pantech_ are reserved. Changes in place.",
				Required:    true,
				Validators:  []validator.String{policyName{}},
			},
			"description": schema.StringAttribute{
				Description: "A note for yourself, at most 255 characters. Defaults to \"\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Validators:  []validator.String{resourcekit.MaxLength(accesspolicy.MaxDescriptionLength)},
			},
			"policy_json": schema.StringAttribute{
				Description: "The statements as JSON, {\"statements\": [...]}, instead of statement blocks. When you use statement blocks, this shows them as JSON, in a normal form (sorted, de-duplicated).",
				Optional:    true,
				Computed:    true,
			},
			"attachment_count": schema.Int64Attribute{
				Description: "How many members and teams the policy is attached to.",
				Computed:    true,
			},
			"version": schema.Int64Attribute{
				Description: "Version of the policy. Every update is sent with it as If-Match, so a change made elsewhere since the last refresh is refused instead of overwritten.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the policy was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
		Blocks: map[string]schema.Block{
			"statement": schema.ListNestedBlock{
				Description: "One statement, 1 to 20 per policy, unless policy_json is set. A matching deny wins over any allow.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"sid":       schema.StringAttribute{Description: accesspolicy.SidDescription, Optional: true},
					"effect":    schema.StringAttribute{Description: accesspolicy.EffectDescription, Required: true, Validators: []validator.String{resourcekit.OneOf("allow", "deny")}},
					"actions":   schema.SetAttribute{Description: accesspolicy.ActionsDescription, Required: true, ElementType: types.StringType},
					"resources": schema.SetAttribute{Description: accesspolicy.ResourcesDescription, Required: true, ElementType: types.StringType},
					"conditions": schema.SingleNestedAttribute{
						Description: accesspolicy.ConditionsDescription,
						Optional:    true,
						Attributes: map[string]schema.Attribute{
							"source_ip": schema.SetAttribute{Description: accesspolicy.SourceIPDescription, Optional: true, ElementType: types.StringType},
							"mfa":       schema.BoolAttribute{Description: accesspolicy.MFADescription, Optional: true},
						},
					},
				}},
			},
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(policyAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ValidateConfig requires exactly one of statement blocks and policy_json,
// and checks the statements as the backend would.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m model
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hasBlocks, hasJSON := len(m.Statements) > 0, !m.PolicyJSON.IsNull()
	if hasBlocks == hasJSON {
		resp.Diagnostics.AddError("Set exactly one of statement and policy_json",
			"Write the policy's statements either as statement blocks or as policy_json, not both and not neither.")
		return
	}
	statements, known, diags := configuredStatements(ctx, m)
	resp.Diagnostics.Append(diags...)
	if known {
		addProblems(&resp.Diagnostics, hasBlocks, accesspolicy.Validate(statements))
	}
}

// ModifyPlan checks every action against the catalogue the API publishes,
// which grows, and shows policy_json as it will be stored when the statements
// are blocks. It never fails the plan because the catalogue could not be read.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	statements, known, diags := configuredStatements(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if !known || resp.Diagnostics.HasError() || len(accesspolicy.Validate(statements)) > 0 {
		return
	}
	if len(plan.Statements) > 0 {
		if doc, err := accesspolicy.MarshalDocument(accesspolicy.Normalize(statements)); err == nil {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("policy_json"), doc)...)
		}
	}
	r.checkCatalogue(ctx, statements, len(plan.Statements) > 0, &resp.Diagnostics)
}

// checkCatalogue reports an action that matches nothing in GET /iam/actions.
func (r *Resource) checkCatalogue(ctx context.Context, statements []client.PolicyStatement, blocks bool, diags *diag.Diagnostics) {
	if r.api == nil {
		return
	}
	catalogue, err := r.api.ListIAMActions(ctx)
	if err != nil || len(catalogue) == 0 {
		return
	}
	var problems []accesspolicy.Problem
	for i, s := range statements {
		for _, a := range s.Actions {
			if !matchesAny(a, catalogue) {
				problems = append(problems, accesspolicy.Problem{Statement: i, Field: "actions",
					Message: fmt.Sprintf("%q matches no action. The pantechdynamics_iam_actions data source lists every action.", a)})
			}
		}
	}
	addProblems(diags, blocks, problems)
}

func matchesAny(pattern string, catalogue []client.IAMAction) bool {
	for _, a := range catalogue {
		if accesspolicy.MatchesAction(pattern, a.Action) {
			return true
		}
	}
	return false
}

// Create adds the policy.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := toRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.api.CreateIAMPolicy(ctx, body)
	if err != nil {
		r.addAPIError(&resp.Diagnostics, "Error creating access policy", plan, err, createHints)
		return
	}
	r.save(ctx, plan, p, &resp.State, &resp.Diagnostics)
}

// Read refreshes state from the API. A policy deleted outside Terraform is
// removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.api.GetIAMPolicy(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading access policy", err, nil)
		return
	}
	r.save(ctx, state, p, &resp.State, &resp.Diagnostics)
}

// Update replaces the policy, guarded by the version in state.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := toRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.api.UpdateIAMPolicy(ctx, state.ID.ValueString(), state.Version.ValueInt64(), body)
	if err != nil {
		r.addAPIError(&resp.Diagnostics, "Error updating access policy", plan, err, updateHints)
		return
	}
	r.save(ctx, plan, p, &resp.State, &resp.Diagnostics)
}

// Delete removes the policy, which detaches it everywhere. One already gone
// counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.api.DeleteIAMPolicy(ctx, state.ID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting access policy", err, updateHints)
	}
}

// ImportState adopts one of the organization's policies by id. A managed
// policy is refused: it cannot change, so it is not a resource.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.HasPrefix(req.ID, client.ManagedPolicyPrefix) {
		resp.Diagnostics.AddError("Cannot import a managed policy",
			fmt.Sprintf("%s is managed by Pantech and cannot change, so it is not a resource. Read it with the pantechdynamics_access_policy data source and attach it with pantechdynamics_access_policy_attachment.", req.ID))
		return
	}
	if !resourcekit.CheckID(&resp.Diagnostics, kind, idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *Resource) save(ctx context.Context, prev model, p *client.IAMPolicy, st *tfsdk.State, diags *diag.Diagnostics) {
	m, d := fromAPIResponse(ctx, prev, p)
	diags.Append(d...)
	if d.HasError() {
		return
	}
	diags.Append(st.Set(ctx, m)...)
}

// addAPIError reports a refused write. INVALID_POLICY field errors
// (statements[i].actions[j]) point at the statement block, or name the
// statement on policy_json.
func (r *Resource) addAPIError(diags *diag.Diagnostics, summary string, plan model, err error, hints map[string]string) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && len(apiErr.Errors) > 0 {
		for _, fe := range apiErr.Errors {
			p, ok := fieldPath(fe.Field, len(plan.Statements) > 0)
			if !ok {
				diags.AddError(summary, err.Error())
				return
			}
			diags.AddAttributeError(p, summary, fe.Field+": "+apiErr.FieldDetail(fe))
		}
		return
	}
	resourcekit.AddHintedError(diags, summary, err, hints)
}

// fieldPath maps a 422 field to an attribute: name and description as they
// are; statements[i].<field>[j] to the statement block's attribute, or to
// policy_json.
func fieldPath(field string, blocks bool) (path.Path, bool) {
	switch field {
	case "name", "description":
		return path.Root(field), true
	}
	rest, ok := strings.CutPrefix(field, "statements")
	if !ok {
		return path.Path{}, false
	}
	if !blocks {
		return path.Root("policy_json"), true
	}
	var i int
	var sub string
	if _, err := fmt.Sscanf(rest, "[%d]", &i); err != nil {
		return path.Root("statement"), true
	}
	if _, after, found := strings.Cut(rest, "]."); found {
		sub = after
	}
	sub, _, _ = strings.Cut(sub, "[")
	return accesspolicy.ProblemPath("statement", accesspolicy.Problem{Statement: i, Field: sub}), true
}

// addProblems reports plan-time problems on the statement blocks, or on
// policy_json naming the statement.
func addProblems(diags *diag.Diagnostics, blocks bool, problems []accesspolicy.Problem) {
	for _, p := range problems {
		if blocks {
			diags.AddAttributeError(accesspolicy.ProblemPath("statement", p), "Invalid access policy", p.Message)
			continue
		}
		where := "statements"
		if p.Statement >= 0 {
			where = fmt.Sprintf("statements[%d]", p.Statement)
			if p.Field != "" {
				where += "." + p.Field
			}
		}
		diags.AddAttributeError(path.Root("policy_json"), "Invalid access policy", where+": "+p.Message)
	}
}

// policyName checks the name at plan time.
type policyName struct{}

func (policyName) Description(context.Context) string {
	return "must be 1 to 64 letters, digits, spaces, '.', '_' or '-', starting with a letter or digit"
}

func (v policyName) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (policyName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := accesspolicy.NameProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid policy name", problem)
	}
}

// Hints for the refusals a user can act on.
var (
	createHints = map[string]string{
		"IAM_POLICY_NAME_TAKEN":    "The organization already has a policy with this name. Choose another, or import it.",
		"IAM_POLICY_LIMIT_REACHED": "The organization already has the most policies it may have (100). Delete one you no longer use.",
		"FORBIDDEN":                "Only owners and admins can manage access policies. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":       "Managing access policies needs an API key with the write scope.",
	}
	updateHints = map[string]string{
		client.CodeVersionConflict:  "The policy was changed outside Terraform since it was last read. Run `terraform apply` again: the refresh picks up that change first.",
		"IAM_POLICY_NAME_TAKEN":     "The organization already has a policy with this name.",
		client.CodeIAMManagedPolicy: "Pantech's managed policies cannot be changed or deleted.",
		client.CodeIAMAdminTarget:   "The policy is attached to an admin, so only an owner may change or delete it. Use an API key created by an owner.",
		"FORBIDDEN":                 "Only owners and admins can manage access policies. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":        "Managing access policies needs an API key with the write scope.",
		"INSUFFICIENT_DELETE_SCOPE": "Deleting an access policy needs an API key with the delete scope.",
	}
)
