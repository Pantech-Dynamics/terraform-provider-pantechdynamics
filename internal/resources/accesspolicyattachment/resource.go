// Package accesspolicyattachment implements the
// pantechdynamics_access_policy_attachment resource: one policy attached to
// one member or team. It works with the organization's policies and with
// Pantech-managed ones (pol_pantech_*).
package accesspolicyattachment

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

const policyPrefix = "pol_"

// principalPrefixes is the id prefix each principal type takes.
var principalPrefixes = map[string]string{"member": "usr_", "team": "team_"}

// attachmentAPI is the part of the API client this resource needs.
type attachmentAPI interface {
	ListIAMPolicyAttachments(ctx context.Context, policyID string) ([]client.IAMAttachment, error)
	AttachIAMPolicy(ctx context.Context, policyID, principalType, principalID string) (*client.IAMAttachment, error)
	DetachIAMPolicy(ctx context.Context, policyID, principalType, principalID string) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one attachment.
type Resource struct {
	api attachmentAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

type model struct {
	ID            types.String `tfsdk:"id"`
	PolicyID      types.String `tfsdk:"policy_id"`
	PrincipalType types.String `tfsdk:"principal_type"`
	PrincipalID   types.String `tfsdk:"principal_id"`
	AttachedAt    types.String `tfsdk:"attached_at"`
}

// Metadata sets the type name: pantechdynamics_access_policy_attachment.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_policy_attachment"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Attaches an access policy, your own or a Pantech-managed one (pol_pantech_*), to a member or a team. Every argument replaces the attachment. " +
			"Destroying it detaches the policy. At most 10 policies per member and per team. Only an owner may attach a policy to an admin, or to a team holding one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "\"<policy_id>/<principal_type>/<principal_id>\".",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"policy_id": schema.StringAttribute{
				Description: "Id of the policy (pol_..., or a managed pol_pantech_... such as pol_pantech_developer).", Required: true,
				PlanModifiers: replace, Validators: []validator.String{resourcekit.IDPrefix("access policy", policyPrefix)},
			},
			"principal_type": schema.StringAttribute{
				Description: "\"member\" or \"team\".", Required: true,
				PlanModifiers: replace, Validators: []validator.String{resourcekit.OneOf("member", "team")},
			},
			"principal_id": schema.StringAttribute{
				Description:   "Id of the member (usr_...) or the team (team_...).",
				Required:      true,
				PlanModifiers: replace,
			},
			"attached_at": schema.StringAttribute{
				Description:   "When the policy was attached, in RFC 3339 UTC, to the second.",
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
	api, ok := req.ProviderData.(attachmentAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ValidateConfig checks that principal_id fits principal_type.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m model
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.PrincipalType.IsUnknown() || m.PrincipalID.IsUnknown() || m.PrincipalType.IsNull() || m.PrincipalID.IsNull() {
		return
	}
	prefix, ok := principalPrefixes[m.PrincipalType.ValueString()]
	if ok && !strings.HasPrefix(m.PrincipalID.ValueString(), prefix) {
		resp.Diagnostics.AddAttributeError(path.Root("principal_id"), "Invalid principal id",
			fmt.Sprintf("A %s id starts with %s, got %q.", m.PrincipalType.ValueString(), prefix, m.PrincipalID.ValueString()))
	}
}

// Create attaches the policy. A policy already attached to the principal is
// adopted: the API answers it with 200.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	policyID, typ, principal := plan.PolicyID.ValueString(), plan.PrincipalType.ValueString(), plan.PrincipalID.ValueString()
	a, err := r.api.AttachIAMPolicy(ctx, policyID, typ, principal)
	if err != nil {
		// The API answers 200 with an attachment that already exists, so a
		// refusal here is real; it is still checked against the attachments
		// before failing, in case the refused call raced another one.
		existing, findErr := r.find(ctx, policyID, typ, principal)
		if findErr != nil {
			resourcekit.AddHintedError(&resp.Diagnostics, "Error attaching access policy", err, hints)
			return
		}
		a = existing
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(a))...)
}

// Read refreshes state from the policy's attachments. An attachment, or a
// policy, gone outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.find(ctx, state.PolicyID.ValueString(), state.PrincipalType.ValueString(), state.PrincipalID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading access policy attachment", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(a))...)
}

// Update is never asked to change anything: every argument replaces the
// attachment.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete detaches the policy. Not attached counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.api.DetachIAMPolicy(ctx, state.PolicyID.ValueString(), state.PrincipalType.ValueString(), state.PrincipalID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error detaching access policy", err, hints)
	}
}

// ImportState adopts an attachment by "<policy_id>/<principal_type>/<principal_id>".
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	prefix, typeOK := "", false
	if len(parts) == 3 {
		prefix, typeOK = principalPrefixes[parts[1]]
	}
	if !typeOK || !strings.HasPrefix(parts[0], policyPrefix) || !strings.HasPrefix(parts[2], prefix) {
		resp.Diagnostics.AddError("Invalid import id",
			fmt.Sprintf("Expected \"<policy_id>/<principal_type>/<principal_id>\", such as \"pol_pantech_developer/team/team_1\" or \"pol_1/member/usr_1\", got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal_type"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal_id"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// find returns the attachment from the policy's list, or ErrNotFound when it
// or the policy is gone.
func (r *Resource) find(ctx context.Context, policyID, typ, principal string) (*client.IAMAttachment, error) {
	attachments, err := r.api.ListIAMPolicyAttachments(ctx, policyID)
	if err != nil {
		return nil, err
	}
	for i := range attachments {
		if attachments[i].PrincipalType == typ && attachments[i].PrincipalID == principal {
			return &attachments[i], nil
		}
	}
	return nil, fmt.Errorf("attachment of %s to %s %s: %w", policyID, typ, principal, client.ErrNotFound)
}

func fromAPIResponse(a *client.IAMAttachment) model {
	return model{
		ID:       types.StringValue(a.PolicyID + "/" + a.PrincipalType + "/" + a.PrincipalID),
		PolicyID: types.StringValue(a.PolicyID), PrincipalType: types.StringValue(a.PrincipalType), PrincipalID: types.StringValue(a.PrincipalID),
		AttachedAt: resourcekit.Timestamp(a.AttachedAt),
	}
}

// hints explain the refusals a user can act on.
var hints = map[string]string{
	"IAM_ATTACHMENT_LIMIT_REACHED": "The member or team already has the most policies it may have (10). Detach one, or combine policies.",
	client.CodeIAMAdminTarget:      "Only an owner may attach a policy to an admin, or to a team holding one, or detach it. Use an API key created by an owner.",
	client.CodeIAMPolicyNotFound:   "The policy does not exist. Check policy_id.",
	client.CodeIAMTeamNotFound:     "The team does not exist. Check principal_id.",
	"FORBIDDEN":                    "Only owners and admins can attach policies. Use an API key created by one.",
	"INSUFFICIENT_SCOPE":           "Attaching policies needs an API key with the write scope.",
}
