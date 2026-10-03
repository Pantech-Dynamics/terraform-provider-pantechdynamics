package images

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the data source's attributes to Go.
type model struct {
	ID     types.String `tfsdk:"id"`
	Images []imageModel `tfsdk:"images"`
}

type imageModel struct {
	ID      types.String   `tfsdk:"id"`
	Slug    types.String   `tfsdk:"slug"`
	Name    types.String   `tfsdk:"name"`
	Version types.String   `tfsdk:"version"`
	Zones   []types.String `tfsdk:"zones"`
}

// fromAPIResponse builds the state from the API's images.
func fromAPIResponse(images []client.Image) model {
	m := model{ID: types.StringValue("images"), Images: make([]imageModel, 0, len(images))}
	for _, img := range images {
		zones := make([]types.String, 0, len(img.Zones))
		for _, z := range img.Zones {
			zones = append(zones, types.StringValue(z))
		}
		m.Images = append(m.Images, imageModel{
			ID:      types.StringValue(img.ID),
			Slug:    types.StringValue(img.Slug),
			Name:    types.StringValue(img.Name),
			Version: types.StringValue(img.Version),
			Zones:   zones,
		})
	}
	return m
}
