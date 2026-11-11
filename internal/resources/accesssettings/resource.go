// Package accesssettings implements the pantechdynamics_access_settings
// resource: the organization's member_baseline.
package accesssettings

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// settingsID is the fixed id of the organization's one access setting.
const settingsID = "access_settings"

// settingsAPI is the part of the API client this resource needs.
type settingsAPI interface {
	GetAccessSettings(ctx context.Context) (*client.AccessSettings, error)
	SetAccessSettings(ctx context.Context, memberBaseline string) (*client.AccessSettings, error)
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages the organization's access setting.
type Resource struct {
	api settingsAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

type model struct {
	ID             types.String `tfsdk:"id"`
	MemberBaseline types.String `tfsdk:"member_baseline"`
	Version        types.Int64  `tfsdk:"version"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

// Metadata sets the type name: pantechdynamics_access_settings.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_settings"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Your organization's access setting. There is one per organization, so declare this resource once. Only an owner's API key can change it, and owners are emailed when it changes. " +
			"Destroying it leaves the setting as it is (with a warning); set member_baseline = \"read\" first to go back to the default.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always \"access_settings\".",
				Computed:    true,
			},
			"member_baseline": schema.StringAttribute{
				Description: "What members (not owners or admins) may do without a policy: \"read\" (view everything, the default) or \"none\" (only what their policies allow).",
				Required:    true,
				Validators:  []validator.String{resourcekit.OneOf("read", "none")},
			},
			"version": schema.Int64Attribute{
				Description: "Version of the setting.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "When the setting last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(settingsAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create sets the baseline: the setting always exists, so this takes it over.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.set(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

// Read refreshes state.
func (r *Resource) Read(ctx context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	s, err := r.api.GetAccessSettings(ctx)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading access settings", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(s))...)
}

// Update changes the baseline.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.set(ctx, req.Plan, &resp.State, &resp.Diagnostics)
}

// Delete leaves the setting unchanged: there is no "unset", and quietly
// resetting it would widen what members may do.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.AddWarning("Access settings left unchanged",
		"Destroying pantechdynamics_access_settings only removes it from Terraform state. member_baseline stays \""+state.MemberBaseline.ValueString()+"\". "+
			"To go back to the default, set member_baseline = \"read\" and apply before destroying.")
}

// ImportState adopts the setting. Any id works; "access_settings" is the
// natural one.
func (r *Resource) ImportState(ctx context.Context, _ resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), settingsID)...)
}

// set sends the PUT and saves the answer.
func (r *Resource) set(ctx context.Context, plan tfsdk.Plan, st *tfsdk.State, diags *diag.Diagnostics) {
	var m model
	diags.Append(plan.Get(ctx, &m)...)
	if diags.HasError() {
		return
	}
	s, err := r.api.SetAccessSettings(ctx, m.MemberBaseline.ValueString())
	if err != nil {
		resourcekit.AddHintedAPIError(diags, "Error setting access settings", err, attributeFor, hints)
		return
	}
	diags.Append(st.Set(ctx, fromAPIResponse(s))...)
}

func fromAPIResponse(s *client.AccessSettings) model {
	return model{
		ID: types.StringValue(settingsID), MemberBaseline: types.StringValue(s.MemberBaseline),
		Version: types.Int64Value(s.Version), UpdatedAt: resourcekit.Timestamp(s.UpdatedAt),
	}
}

func attributeFor(field string) (path.Path, bool) {
	if field == "member_baseline" {
		return path.Root(field), true
	}
	return path.Path{}, false
}

// hints explain the refusals a user can act on.
var hints = map[string]string{
	"FORBIDDEN":          "Only owners can change the access setting. Use an API key created by an owner.",
	"INSUFFICIENT_SCOPE": "Changing the access setting needs an API key with the write scope.",
}
