// Package images implements the pantechdynamics_images data source.
package images

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// imageAPI is the part of the API client this data source needs, defined here by
// the consumer so tests can fake it.
type imageAPI interface {
	ListImages(ctx context.Context) ([]client.Image, error)
}

var (
	_ datasource.DataSource              = &DataSource{}
	_ datasource.DataSourceWithConfigure = &DataSource{}
)

// DataSource lists the operating system images a customer can choose.
type DataSource struct {
	api imageAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource {
	return &DataSource{}
}

// Metadata sets the type name: pantechdynamics_images.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the operating system images available for instances. Use an image's slug as the image when creating an instance, and check that its zones include the zone you are creating in.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always \"images\". Present because every data source needs an id.",
				Computed:    true,
			},
			"images": schema.ListNestedAttribute{
				Description: "The available images.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":      schema.StringAttribute{Computed: true, Description: "Identifier of the image."},
						"slug":    schema.StringAttribute{Computed: true, Description: "Stable short name of the image, used when creating an instance."},
						"name":    schema.StringAttribute{Computed: true, Description: "Operating system name, for example Ubuntu."},
						"version": schema.StringAttribute{Computed: true, Description: "Operating system version, for example 24.04 LTS."},
						"zones": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "Zones the image can be used in.",
						},
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
	api, ok := req.ProviderData.(imageAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.api = api
}

// Read fetches the images and stores them in state.
func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	images, err := d.api.ListImages(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading images", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(images))...)
}
