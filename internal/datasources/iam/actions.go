package iam

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// actionsAPI is the part of the API client the catalogue needs.
type actionsAPI interface {
	ListIAMActions(ctx context.Context) ([]client.IAMAction, error)
}

var _ datasource.DataSourceWithConfigure = &ActionsDataSource{}

// ActionsDataSource lists the action catalogue.
type ActionsDataSource struct {
	api actionsAPI
}

// NewActions is the factory the provider registers.
func NewActions() datasource.DataSource { return &ActionsDataSource{} }

type actionsModel struct {
	ID      types.String  `tfsdk:"id"`
	Service types.String  `tfsdk:"service"`
	Actions []actionModel `tfsdk:"actions"`
}

type actionModel struct {
	Action        types.String `tfsdk:"action"`
	Service       types.String `tfsdk:"service"`
	Description   types.String `tfsdk:"description"`
	ResourceTypes types.List   `tfsdk:"resource_types"`
	Read          types.Bool   `tfsdk:"read"`
}

// Metadata sets the type name: pantechdynamics_iam_actions.
func (d *ActionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_actions"
}

// Schema describes the attributes.
func (d *ActionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists every action an access policy statement can name, such as instances:Stop, with what it allows. Any member may read it.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, Description: "\"iam_actions\", or \"iam_actions/<service>\" when filtered."},
			"service": schema.StringAttribute{Optional: true, Description: "Only this service's actions, such as \"instances\" or \"dns\"."},
			"actions": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The actions, in the order the API lists them.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"action":         schema.StringAttribute{Computed: true, Description: "The action, <service>:<verb>, such as \"instances:Stop\"."},
					"service":        schema.StringAttribute{Computed: true, Description: "The service, such as \"instances\"."},
					"description":    schema.StringAttribute{Computed: true, Description: "What the action allows."},
					"resource_types": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "The kinds of resource the action applies to; empty for actions that are not about one resource."},
					"read":           schema.BoolAttribute{Computed: true, Description: "Whether it only reads. Members get read actions without a policy while member_baseline is \"read\"."},
				}},
			},
		},
	}
}

// Configure receives the API client from the provider.
func (d *ActionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(actionsAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// Read fetches the catalogue.
func (d *ActionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg actionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	catalogue, err := d.api.ListIAMActions(ctx)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading the action catalogue", err, nil)
		return
	}
	service := cfg.Service.ValueString()
	m := actionsModel{ID: types.StringValue(strings.TrimSuffix("iam_actions/"+service, "/")), Service: cfg.Service, Actions: []actionModel{}}
	for _, a := range catalogue {
		if service != "" && a.Service != service {
			continue
		}
		kinds := a.ResourceTypes
		if kinds == nil {
			kinds = []string{}
		}
		list, diags := types.ListValueFrom(ctx, types.StringType, kinds)
		resp.Diagnostics.Append(diags...)
		m.Actions = append(m.Actions, actionModel{Action: types.StringValue(a.Action), Service: types.StringValue(a.Service),
			Description: types.StringValue(a.Description), ResourceTypes: list, Read: types.BoolValue(a.Read)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
