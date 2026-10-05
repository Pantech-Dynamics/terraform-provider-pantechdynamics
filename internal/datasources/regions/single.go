package regions

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
)

var (
	_ datasource.DataSource              = &SingleDataSource{}
	_ datasource.DataSourceWithConfigure = &SingleDataSource{}
)

// SingleDataSource looks up one region by its code.
type SingleDataSource struct {
	api regionAPI
}

// NewSingle is the factory the provider registers.
func NewSingle() datasource.DataSource {
	return &SingleDataSource{}
}

// singleModel maps the data source's attributes to Go.
type singleModel struct {
	Code       types.String     `tfsdk:"code"`
	ID         types.String     `tfsdk:"id"`
	Name       types.String     `tfsdk:"name"`
	Placements []placementModel `tfsdk:"placements"`
}

// Metadata sets the type name: pantechdynamics_region.
func (d *SingleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_region"
}

// Schema describes the attributes: code goes in, the rest comes out.
func (d *SingleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := regionAttributes()
	attrs["code"] = schema.StringAttribute{
		Description: "Code of the region to look up, for example \"af-abj\". It must match exactly. The lookup fails if no region has it.",
		Required:    true,
	}
	attrs["id"] = schema.StringAttribute{Computed: true, Description: "The region code. Present because every data source needs an id."}
	resp.Schema = schema.Schema{
		Description: "Looks up one region by code. Use it to fail early on a wrong region, and to read where each kind of instance lands there and whether your account can create one now. Use pantechdynamics_regions to list them all.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *SingleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(regionAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.api = api
}

// Read fetches the regions and picks the one with the code.
func (d *SingleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config singleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	regions, err := d.api.ListRegions(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading regions", err.Error())
		return
	}

	code := config.Code.ValueString()
	i := slices.IndexFunc(regions, func(r client.Region) bool { return r.Code == code })
	if i < 0 {
		codes := make([]string, 0, len(regions))
		for _, r := range regions {
			codes = append(codes, r.Code)
		}
		resp.Diagnostics.AddAttributeError(path.Root("code"), "Region not found", lookup.NotFound("region", "code", code, "", codes))
		return
	}
	m := fromAPIResponse(regions[i : i+1]).Regions[0]
	resp.Diagnostics.Append(resp.State.Set(ctx, singleModel{Code: m.Code, ID: m.Code, Name: m.Name, Placements: m.Placements})...)
}
