package team

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// membershipAPI is the part of the API client the membership resource needs.
type membershipAPI interface {
	ListIAMTeamMembers(ctx context.Context, teamID string) ([]client.IAMTeamMember, error)
	AddIAMTeamMember(ctx context.Context, teamID, userID string) (*client.IAMTeamMember, error)
	RemoveIAMTeamMember(ctx context.Context, teamID, userID string) error
}

var (
	_ resource.Resource                = &MembershipResource{}
	_ resource.ResourceWithConfigure   = &MembershipResource{}
	_ resource.ResourceWithImportState = &MembershipResource{}
)

// MembershipResource manages one member of one team. One resource per
// membership, so memberships managed elsewhere (the console, another
// configuration) are left alone.
type MembershipResource struct {
	api membershipAPI
}

// NewMembership is the factory the provider registers.
func NewMembership() resource.Resource { return &MembershipResource{} }

type membershipModel struct {
	ID      types.String `tfsdk:"id"`
	TeamID  types.String `tfsdk:"team_id"`
	UserID  types.String `tfsdk:"user_id"`
	Email   types.String `tfsdk:"email"`
	Role    types.String `tfsdk:"role"`
	AddedAt types.String `tfsdk:"added_at"`
}

// Metadata sets the type name: pantechdynamics_team_membership.
func (r *MembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_membership"
}

// Schema describes the attributes.
func (r *MembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "One member of one team. The member must already be in your organization (invite them in the console). " +
			"Destroying it takes the member out of the team; they stay in the organization. Admins can add and remove members only; adding an admin to a team needs an owner's API key.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "\"<team_id>/<user_id>\".",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"team_id": schema.StringAttribute{
				Description: "Id of the team (team_...). Changing it moves the member: out of the old team, into the new one.", Required: true,
				PlanModifiers: replace, Validators: []validator.String{resourcekit.IDPrefix("team", teamPrefix)},
			},
			"user_id": schema.StringAttribute{
				Description: "Id of the member (usr_...). Changing it replaces the membership.", Required: true,
				PlanModifiers: replace, Validators: []validator.String{resourcekit.IDPrefix("user", userPrefix)},
			},
			"email": schema.StringAttribute{
				Description:   "The member's email address.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"role": schema.StringAttribute{
				Description: "The member's role in the organization: \"owner\", \"admin\" or \"member\".",
				Computed:    true,
			},
			"added_at": schema.StringAttribute{
				Description:   "When the member was added to the team, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// Configure receives the API client built by the provider.
func (r *MembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(membershipAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create adds the member to the team. Adding a member already in it is
// accepted, which adopts the membership.
func (r *MembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	teamID, userID := plan.TeamID.ValueString(), plan.UserID.ValueString()
	m, err := r.api.AddIAMTeamMember(ctx, teamID, userID)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error adding team member", err, membershipHints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, membershipFromAPI(teamID, m))...)
}

// Read refreshes state from the team's member list. A member, or a team,
// gone outside Terraform is removed from state.
func (r *MembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	teamID := state.TeamID.ValueString()
	m, err := r.find(ctx, teamID, state.UserID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading team member", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, membershipFromAPI(teamID, m))...)
}

// Update is never asked to change anything: both arguments replace the
// membership.
func (r *MembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete takes the member out of the team. Already out counts as success.
func (r *MembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.api.RemoveIAMTeamMember(ctx, state.TeamID.ValueString(), state.UserID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error removing team member", err, membershipHints)
	}
}

// ImportState adopts a membership by "<team_id>/<user_id>".
func (r *MembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	teamID, userID, ok := strings.Cut(req.ID, "/")
	if !ok || !strings.HasPrefix(teamID, teamPrefix) || !strings.HasPrefix(userID, userPrefix) || strings.Contains(userID, "/") {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("Expected \"<team_id>/<user_id>\", such as \"team_1/usr_1\", got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), teamID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), userID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// find returns the member from the team's list, or ErrNotFound when the
// member or the team is gone.
func (r *MembershipResource) find(ctx context.Context, teamID, userID string) (*client.IAMTeamMember, error) {
	members, err := r.api.ListIAMTeamMembers(ctx, teamID)
	if err != nil {
		return nil, err
	}
	for i := range members {
		if members[i].UserID == userID {
			return &members[i], nil
		}
	}
	return nil, fmt.Errorf("member %s of team %s: %w", userID, teamID, client.ErrNotFound)
}

func membershipFromAPI(teamID string, m *client.IAMTeamMember) membershipModel {
	return membershipModel{
		ID: types.StringValue(teamID + "/" + m.UserID), TeamID: types.StringValue(teamID), UserID: types.StringValue(m.UserID),
		Email: types.StringValue(m.Email), Role: types.StringValue(m.Role), AddedAt: resourcekit.Timestamp(m.AddedAt),
	}
}

// membershipHints explain the refusals a user can act on.
var membershipHints = map[string]string{
	client.CodeIAMTeamNotFound: "The team does not exist. Check team_id.",
	"RESOURCE_NOT_FOUND":       "The team or the member does not exist. The member must already be in your organization.",
	client.CodeIAMAdminTarget:  "Only an owner may add an admin to a team, or remove one. Use an API key created by an owner.",
	"FORBIDDEN":                "Only owners and admins can change teams. Use an API key created by one.",
	"INSUFFICIENT_SCOPE":       "Changing teams needs an API key with the write scope.",
}
