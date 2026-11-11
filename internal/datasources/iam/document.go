// Package iam implements the access data sources:
// pantechdynamics_access_policy (one policy, managed ones included),
// pantechdynamics_iam_actions (the action catalogue) and
// pantechdynamics_access_policy_document (statement blocks to policy JSON,
// computed locally).
package iam

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/accesspolicy"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

var _ datasource.DataSourceWithValidateConfig = &DocumentDataSource{}

// DocumentDataSource builds policy JSON from statement blocks. It calls no
// API, so it works in any plan, and its output does not change unless the
// statements do.
type DocumentDataSource struct{}

// NewDocument is the factory the provider registers.
func NewDocument() datasource.DataSource { return &DocumentDataSource{} }

type documentModel struct {
	ID         types.String                  `tfsdk:"id"`
	Statements []accesspolicy.StatementModel `tfsdk:"statement"`
	JSON       types.String                  `tfsdk:"json"`
}

// Metadata sets the type name: pantechdynamics_access_policy_document.
func (d *DocumentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_policy_document"
}

// Schema describes the attributes.
func (d *DocumentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Builds an access policy's JSON from statement blocks, checked as the API would check them, without calling the API. " +
			"Use json as pantechdynamics_access_policy's policy_json, for example to build a policy from loops or to share statements between policies. " +
			"The JSON is in a normal form (actions, resources and CIDRs sorted and de-duplicated), so it only changes when the statements' meaning does.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, Description: "Always \"access_policy_document\"."},
			"json": schema.StringAttribute{Computed: true, Description: "The policy JSON, {\"statements\": [...]}."},
		},
		Blocks: map[string]schema.Block{
			"statement": schema.ListNestedBlock{
				Description: "One statement, 1 to 20.",
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

// ValidateConfig checks the statements at plan time.
func (d *DocumentDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var m documentModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !accesspolicy.Known(m.Statements) {
		return
	}
	statements, diags := accesspolicy.ToAPI(ctx, m.Statements)
	resp.Diagnostics.Append(diags...)
	addProblems(&resp.Diagnostics, accesspolicy.Validate(statements))
}

// Read builds the JSON.
func (d *DocumentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m documentModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	statements, diags := accesspolicy.ToAPI(ctx, m.Statements)
	resp.Diagnostics.Append(diags...)
	addProblems(&resp.Diagnostics, accesspolicy.Validate(statements))
	if resp.Diagnostics.HasError() {
		return
	}
	doc, err := accesspolicy.MarshalDocument(accesspolicy.Normalize(statements))
	if err != nil {
		resp.Diagnostics.AddError("Error encoding policy JSON", err.Error())
		return
	}
	m.ID, m.JSON = types.StringValue("access_policy_document"), types.StringValue(doc)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func addProblems(diags *diag.Diagnostics, problems []accesspolicy.Problem) {
	for _, p := range problems {
		diags.AddAttributeError(accesspolicy.ProblemPath("statement", p), "Invalid access policy", p.Message)
	}
}
