// Package diskofferings implements the pantechdynamics_disk_offerings data source.
package diskofferings

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// offeringAPI is the part of the API client this data source needs, defined here
// by the consumer so tests can fake it.
type offeringAPI interface {
	ListDiskOfferings(ctx context.Context) ([]client.DiskOffering, error)
}

var (
	_ datasource.DataSource              = &DataSource{}
	_ datasource.DataSourceWithConfigure = &DataSource{}
)

// DataSource lists the disk offerings a volume can be ordered with.
type DataSource struct {
	api offeringAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource {
	return &DataSource{}
}

// Metadata sets the type name: pantechdynamics_disk_offerings.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_disk_offerings"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the disk offerings a volume can be ordered with. Use an offering's slug as the disk_offering_slug of a pantechdynamics_volume. The storage type matters: on staging, volumes with local storage attached to instances and shared ones did not.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always \"disk_offerings\". Present because every data source needs an id.",
				Computed:    true,
			},
			"disk_offerings": schema.ListNestedAttribute{
				Description: "The available disk offerings.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"slug":               schema.StringAttribute{Computed: true, Description: "Stable short name of the offering, used as disk_offering_slug."},
						"name":               schema.StringAttribute{Computed: true, Description: "Display name of the offering."},
						"size_gb":            schema.Int64Attribute{Computed: true, Description: "Size in gigabytes for a fixed offering, or null for a customized one."},
						"custom_size":        schema.BoolAttribute{Computed: true, Description: "True when the size is chosen with size_gb when the volume is ordered."},
						"storage_type":       schema.StringAttribute{Computed: true, Description: "\"shared\" or \"local\"."},
						"currency":           schema.StringAttribute{Computed: true, Description: "Currency of the price."},
						"hourly_price_minor": schema.Int64Attribute{Computed: true, Description: "Price per hour, in minor currency units (for example kobo). For a customized offering this is the price per gigabyte per hour."},
					},
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
	api, ok := req.ProviderData.(offeringAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.api = api
}

// Read fetches the offerings and stores them in state.
func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	offerings, err := d.api.ListDiskOfferings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading disk offerings", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(offerings))...)
}
