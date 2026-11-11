package containerrepositories

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

var (
	_ datasource.DataSourceWithConfigure      = &SingleDataSource{}
	_ datasource.DataSourceWithValidateConfig = &SingleDataSource{}
)

// SingleDataSource looks up one repository by id or name.
type SingleDataSource struct {
	api repositoryAPI
}

// NewSingle is the factory the provider registers.
func NewSingle() datasource.DataSource { return &SingleDataSource{} }

// Metadata sets the type name: pantechdynamics_container_repository.
func (d *SingleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_repository"
}

// Schema describes the attributes.
func (d *SingleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := detailAttributes()
	attrs["id"] = schema.StringAttribute{Optional: true, Computed: true, Description: "Id of the repository to look up, starting with crr_. Set exactly one of id and name."}
	attrs["name"] = schema.StringAttribute{Optional: true, Computed: true, Description: "Name of the repository inside the namespace, such as \"api/server\". It must match exactly; names are unique in the namespace. Set exactly one of id and name."}
	resp.Schema = schema.Schema{
		Description: "Looks up one repository in your organization's container registry namespace by id or name, for example to get its full_name for an image reference without managing it.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *SingleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configure(req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *SingleDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	lookup.ValidateIDOrName(ctx, req.Config, "container repository", &resp.Diagnostics)
}

// Read lists the repositories (narrowed by name when looking one up by name)
// and picks the one asked for.
func (d *SingleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg repositoryModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, name := cfg.ID.ValueString(), cfg.Name.ValueString()
	repos, err := d.api.ListRepositories(ctx, name)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading container repositories", err, readHints)
		return
	}
	repo, err := lookup.Pick(repos, "container repository", id, name,
		func(r client.Repository) string { return r.ID }, func(r client.Repository) string { return r.Name })
	if err != nil {
		resp.Diagnostics.AddError("Container repository not found", err.Error())
		return
	}
	m, diags := fromAPIResponse(&repo)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
