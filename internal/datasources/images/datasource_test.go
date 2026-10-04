package images

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	images []client.Image
	err    error
}

func (f *fakeAPI) ListImages(context.Context) ([]client.Image, error) { return f.images, f.err }

func read(t *testing.T, api imageAPI) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	New().Schema(ctx, datasource.SchemaRequest{}, &sresp)
	typ := sresp.Schema.Type().TerraformType(ctx)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&DataSource{api: api}).Read(ctx, datasource.ReadRequest{}, &resp)
	return resp
}

func TestSchemaIsValid(t *testing.T) {
	var resp datasource.SchemaResponse
	New().Schema(ctx, datasource.SchemaRequest{}, &resp)
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func TestRead(t *testing.T) {
	api := &fakeAPI{images: []client.Image{
		{ID: "img_1", Slug: "ubuntu-24-04", Name: "Ubuntu", Version: "24.04 LTS", Zones: []string{"af-abj-1", "af-abj-2"}},
		{ID: "img_2", Slug: "debian-12", Name: "Debian", Version: "12"},
	}}

	resp := read(t, api)

	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if got.ID.ValueString() != "images" || len(got.Images) != 2 {
		t.Fatalf("state = %+v", got)
	}
	ubuntu := got.Images[0]
	if ubuntu.Slug.ValueString() != "ubuntu-24-04" || ubuntu.Version.ValueString() != "24.04 LTS" ||
		len(ubuntu.Zones) != 2 || ubuntu.Zones[1].ValueString() != "af-abj-2" {
		t.Fatalf("ubuntu = %+v", ubuntu)
	}
	if got.Images[1].Zones == nil || len(got.Images[1].Zones) != 0 {
		t.Fatalf("an image with no zones must have an empty list, got %#v", got.Images[1].Zones)
	}
}

func TestReadEmptyAndError(t *testing.T) {
	resp := read(t, &fakeAPI{})
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if resp.Diagnostics.HasError() || got.Images == nil || len(got.Images) != 0 {
		t.Fatalf("diags = %v, images = %#v", resp.Diagnostics, got.Images)
	}

	errResp := read(t, &fakeAPI{err: &client.APIError{Status: 500}})
	if !errResp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
}

func TestConfigure(t *testing.T) {
	var bad datasource.ConfigureResponse
	(&DataSource{}).Configure(ctx, datasource.ConfigureRequest{ProviderData: 42}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Fatal("wrong type must error")
	}
	var nilData datasource.ConfigureResponse
	(&DataSource{}).Configure(ctx, datasource.ConfigureRequest{}, &nilData)
	if nilData.Diagnostics.HasError() {
		t.Fatal("nil provider data must be ignored")
	}
}
