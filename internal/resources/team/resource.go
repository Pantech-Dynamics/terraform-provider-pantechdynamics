// Package team implements the pantechdynamics_team resource and the
// pantechdynamics_team_membership resource (one member of one team).
package team

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	teamPrefix = "team_"
	userPrefix = "usr_"

	maxNameLength        = 64
	maxDescriptionLength = 255
)

// teamAPI is the part of the API client the team resource needs.
type teamAPI interface {
	CreateIAMTeam(ctx context.Context, req client.CreateIAMTeamRequest) (*client.IAMTeam, error)
	GetIAMTeam(ctx context.Context, id string) (*client.IAMTeam, error)
	UpdateIAMTeam(ctx context.Context, id string, req client.UpdateIAMTeamRequest) (*client.IAMTeam, error)
	DeleteIAMTeam(ctx context.Context, id string) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one team.
type Resource struct {
	api teamAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

type model struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	MemberCount types.Int64  `tfsdk:"member_count"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_team.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A team: a named group of your organization's members, to attach access policies to once for all of them. " +
			"Add members with pantechdynamics_team_membership and policies with pantechdynamics_access_policy_attachment. Deleting a team removes its members and attachments, not the members themselves. " +
			"At most 50 teams per organization; owners and admins only.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the team, starting with team_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description: "A name unique in the organization, 1 to 64 printable characters. Changes in place.",
				Required:    true,
				Validators:  []validator.String{teamName{}},
			},
			"description": schema.StringAttribute{
				Description: "A note for yourself, at most 255 characters. Defaults to \"\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Validators:  []validator.String{resourcekit.MaxLength(maxDescriptionLength)},
			},
			"member_count": schema.Int64Attribute{
				Description: "How many members the team has.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the team was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(teamAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create adds the team.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.api.CreateIAMTeam(ctx, client.CreateIAMTeamRequest{Name: plan.Name.ValueString(), Description: plan.Description.ValueString()})
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating team", err, attributeFor, teamHints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(t))...)
}

// Read refreshes state. A team deleted outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.api.GetIAMTeam(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading team", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(t))...)
}

// Update renames the team or changes its description.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body client.UpdateIAMTeamRequest
	if !plan.Name.Equal(state.Name) {
		body.Name = plan.Name.ValueStringPointer()
	}
	if !plan.Description.Equal(state.Description) {
		body.Description = plan.Description.ValueStringPointer()
	}
	t, err := r.api.UpdateIAMTeam(ctx, state.ID.ValueString(), body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating team", err, attributeFor, teamHints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(t))...)
}

// Delete removes the team. One already gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.api.DeleteIAMTeam(ctx, state.ID.ValueString()); err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting team", err, teamHints)
	}
}

// ImportState adopts a team by id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, "team", teamPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func fromAPIResponse(t *client.IAMTeam) model {
	return model{
		ID: types.StringValue(t.ID), Name: types.StringValue(t.Name), Description: types.StringValue(t.Description),
		MemberCount: types.Int64Value(t.MemberCount), CreatedAt: resourcekit.Timestamp(t.CreatedAt),
	}
}

func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "description":
		return path.Root(field), true
	default:
		return path.Path{}, false
	}
}

// teamHints explain the refusals a user can act on.
var teamHints = map[string]string{
	"IAM_TEAM_NAME_TAKEN":       "The organization already has a team with this name. Choose another, or import it.",
	"IAM_TEAM_LIMIT_REACHED":    "The organization already has the most teams it may have (50). Delete one you no longer use.",
	client.CodeIAMAdminTarget:   "The team holds an admin, so only an owner may change it. Use an API key created by an owner.",
	"FORBIDDEN":                 "Only owners and admins can manage teams. Use an API key created by one.",
	"INSUFFICIENT_SCOPE":        "Managing teams needs an API key with the write scope.",
	"INSUFFICIENT_DELETE_SCOPE": "Deleting a team needs an API key with the delete scope.",
}

// teamName checks 1 to 64 printable characters with no surrounding space,
// which the backend would trim and the state would then not match.
type teamName struct{}

func (teamName) Description(context.Context) string {
	return fmt.Sprintf("must be 1 to %d printable characters", maxNameLength)
}

func (v teamName) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (teamName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	name := req.ConfigValue.ValueString()
	ok := name != "" && utf8.RuneCountInString(name) <= maxNameLength && unicode.IsGraphic(firstRune(name)) && !unicode.IsSpace(firstRune(name)) && !unicode.IsSpace(lastRune(name))
	for _, r := range name {
		ok = ok && unicode.IsPrint(r)
	}
	if !ok {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid team name",
			fmt.Sprintf("A team name is 1 to %d printable characters, without a leading or trailing space; got %q.", maxNameLength, name))
	}
}

func firstRune(s string) rune { r, _ := utf8.DecodeRuneInString(s); return r }
func lastRune(s string) rune  { r, _ := utf8.DecodeLastRuneInString(s); return r }
