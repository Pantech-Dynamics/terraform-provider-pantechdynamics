package iam

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/accesspolicy"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// policyAPI is the part of the API client the policy lookup needs.
type policyAPI interface {
	ListIAMPolicies(ctx context.Context, managed *bool) ([]client.IAMPolicy, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &PolicyDataSource{}
	_ datasource.DataSourceWithValidateConfig = &PolicyDataSource{}
)

// PolicyDataSource looks up one policy, the organization's or Pantech's, by
// id or name.
type PolicyDataSource struct {
	api policyAPI
}

// NewPolicy is the factory the provider registers.
func NewPolicy() datasource.DataSource { return &PolicyDataSource{} }

type policyModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	Managed         types.Bool   `tfsdk:"managed"`
	Statements      types.List   `tfsdk:"statements"`
	PolicyJSON      types.String `tfsdk:"policy_json"`
	AttachmentCount types.Int64  `tfsdk:"attachment_count"`
	Version         types.Int64  `tfsdk:"version"`
}

// Metadata sets the type name: pantechdynamics_access_policy.
func (d *PolicyDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_policy"
}

// Schema describes the attributes.
func (d *PolicyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one access policy by id or name: one of your organization's, or a Pantech-managed one such as pol_pantech_developer or pol_pantech_read_only, " +
			"for example to attach it with pantechdynamics_access_policy_attachment or to reuse its statements.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Optional: true, Computed: true, Description: "Id of the policy, such as pol_01JD... or pol_pantech_developer. Set exactly one of id and name."},
			"name":        schema.StringAttribute{Optional: true, Computed: true, Description: "Name of the policy. It must match exactly; names are unique. Set exactly one of id and name."},
			"description": schema.StringAttribute{Computed: true, Description: "The policy's description."},
			"managed":     schema.BoolAttribute{Computed: true, Description: "True for a Pantech-managed policy, which cannot change."},
			"statements": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The policy's statements, in order.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"sid":       schema.StringAttribute{Computed: true, Description: accesspolicy.SidDescription},
					"effect":    schema.StringAttribute{Computed: true, Description: accesspolicy.EffectDescription},
					"actions":   schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: accesspolicy.ActionsDescription},
					"resources": schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: accesspolicy.ResourcesDescription},
					"conditions": schema.SingleNestedAttribute{Computed: true, Description: accesspolicy.ConditionsDescription, Attributes: map[string]schema.Attribute{
						"source_ip": schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: accesspolicy.SourceIPDescription},
						"mfa":       schema.BoolAttribute{Computed: true, Description: accesspolicy.MFADescription},
					}},
				}},
			},
			"policy_json":      schema.StringAttribute{Computed: true, Description: "The statements as policy JSON, in a normal form, usable as another policy's policy_json."},
			"attachment_count": schema.Int64Attribute{Computed: true, Description: "How many members and teams the policy is attached to."},
			"version":          schema.Int64Attribute{Computed: true, Description: "Version of the policy."},
		},
	}
}

// Configure receives the API client from the provider.
func (d *PolicyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(policyAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// ValidateConfig requires exactly one of id and name.
func (d *PolicyDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	lookup.ValidateIDOrName(ctx, req.Config, "access policy", &resp.Diagnostics)
}

// Read lists every policy, managed ones included, and picks the one asked
// for.
func (d *PolicyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg policyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	policies, err := d.api.ListIAMPolicies(ctx, nil)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading access policies", err, map[string]string{
			"ACCESS_DENIED": "Reading policies needs iam:Read: an owner's or admin's API key, or a member granted iam:Read.",
		})
		return
	}
	p, err := lookup.Pick(policies, "access policy", cfg.ID.ValueString(), cfg.Name.ValueString(),
		func(p client.IAMPolicy) string { return p.ID }, func(p client.IAMPolicy) string { return p.Name })
	if err != nil {
		resp.Diagnostics.AddError("Access policy not found", err.Error())
		return
	}
	statements, diags := accesspolicy.FromAPI(ctx, p.Statements)
	resp.Diagnostics.Append(diags...)
	list, diags := types.ListValueFrom(ctx, accesspolicy.ObjectType, statements)
	resp.Diagnostics.Append(diags...)
	doc, err := accesspolicy.MarshalDocument(accesspolicy.Normalize(p.Statements))
	if err != nil {
		resp.Diagnostics.AddError("Error encoding policy JSON", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, policyModel{
		ID: types.StringValue(p.ID), Name: types.StringValue(p.Name), Description: types.StringValue(p.Description), Managed: types.BoolValue(p.Managed),
		Statements: list, PolicyJSON: types.StringValue(doc), AttachmentCount: types.Int64Value(p.AttachmentCount), Version: types.Int64Value(p.Version),
	})...)
}
