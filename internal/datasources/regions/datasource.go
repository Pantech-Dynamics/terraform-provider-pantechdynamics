// Package regions implements the pantechdynamics_regions data source.
package regions

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// regionAPI is the part of the API client this data source needs, defined here
// by the consumer so tests can fake it.
type regionAPI interface {
	ListRegions(ctx context.Context) ([]client.Region, error)
}

var (
	_ datasource.DataSource              = &DataSource{}
	_ datasource.DataSourceWithConfigure = &DataSource{}
)

// DataSource lists the regions and where each kind of instance lands.
type DataSource struct {
	api regionAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource {
	return &DataSource{}
}

// Metadata sets the type name: pantechdynamics_regions.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_regions"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the regions and, for each, where a new instance of each kind lands and whether your account can create one there right now.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always \"regions\". Present because every data source needs an id.",
				Computed:    true,
			},
			"regions": schema.ListNestedAttribute{
				Description:  "The regions.",
				Computed:     true,
				NestedObject: schema.NestedAttributeObject{Attributes: regionAttributes()},
			},
		},
	}
}

// regionAttributes describes one region. The list and the single-region data
// source share it, so a region reads the same in both.
func regionAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"code": schema.StringAttribute{Computed: true, Description: "Short code of the region, for example af-abj."},
		"name": schema.StringAttribute{Computed: true, Description: "Display name of the region."},
		"placements": schema.ListNestedAttribute{
			Computed:    true,
			Description: "Where each kind of instance lands in this region. A kind the region does not offer is absent.",
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"kind":                 schema.StringAttribute{Computed: true, Description: "\"standard\" is a VPS with a public IP on the shared network. \"vpc\" is a VM in a customer VPC."},
					"zone":                 schema.StringAttribute{Computed: true, Description: "Zone where a new instance of this kind lands. Check an image's zones against it."},
					"available":            schema.BoolAttribute{Computed: true, Description: "Whether your account can create an instance of this kind here now."},
					"unavailable_reason":   schema.StringAttribute{Computed: true, Description: "Why it is unavailable, or null when available."},
					"private_network_cidr": schema.StringAttribute{Computed: true, Description: "Range of the zone's private database network, for example \"10.250.0.0/20\", when a standard instance there can add a private network interface (private_network on pantechdynamics_instance). Null otherwise, and always for \"vpc\". The security group of an instance with that interface must allow nothing from this range."},
				},
			},
		},
	}
}

// Configure receives the API client from the provider. Terraform calls it early
// with no data, so a nil ProviderData is normal and ignored.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read fetches the regions and stores them in state.
func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	regions, err := d.api.ListRegions(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading regions", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(regions))...)
}
