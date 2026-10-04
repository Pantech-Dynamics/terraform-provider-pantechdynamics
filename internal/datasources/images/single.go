package images

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

// SingleDataSource looks up one operating system image by its slug.
type SingleDataSource struct {
	api imageAPI
}

// NewSingle is the factory the provider registers.
func NewSingle() datasource.DataSource {
	return &SingleDataSource{}
}

// singleModel maps the data source's attributes to Go.
type singleModel struct {
	Slug    types.String   `tfsdk:"slug"`
	ID      types.String   `tfsdk:"id"`
	Name    types.String   `tfsdk:"name"`
	Version types.String   `tfsdk:"version"`
	Zones   []types.String `tfsdk:"zones"`
}

// Metadata sets the type name: pantechdynamics_image.
func (d *SingleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

// Schema describes the attributes: slug goes in, the rest comes out.
func (d *SingleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := imageAttributes()
	attrs["slug"] = schema.StringAttribute{
		Description: "Slug of the image to look up, for example \"ubuntu-24-04\". It must match exactly. The lookup fails if no image has it.",
		Required:    true,
	}
	resp.Schema = schema.Schema{
		Description: "Looks up one operating system image by slug. Use it to fail early on a wrong slug, and to read the zones the image can be used in. Use pantechdynamics_images to list them all.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *SingleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read fetches the images and picks the one with the slug.
func (d *SingleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config singleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	images, err := d.api.ListImages(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading images", err.Error())
		return
	}

	slug := config.Slug.ValueString()
	i := slices.IndexFunc(images, func(img client.Image) bool { return img.Slug == slug })
	if i < 0 {
		slugs := make([]string, 0, len(images))
		for _, img := range images {
			slugs = append(slugs, img.Slug)
		}
		resp.Diagnostics.AddAttributeError(path.Root("slug"), "Image not found", lookup.NotFound("image", "slug", slug, "", slugs))
		return
	}
	m := fromAPIResponse(images[i : i+1]).Images[0]
	resp.Diagnostics.Append(resp.State.Set(ctx, singleModel{Slug: m.Slug, ID: m.ID, Name: m.Name, Version: m.Version, Zones: m.Zones})...)
}
