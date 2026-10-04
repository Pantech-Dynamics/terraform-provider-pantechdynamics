package images

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func readOne(t *testing.T, api imageAPI, slug string) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	NewSingle().Schema(ctx, datasource.SchemaRequest{}, &sresp)
	s := sresp.Schema
	typ, ok := s.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("the schema type is not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["slug"] = tftypes.NewValue(tftypes.String, slug)

	resp := datasource.ReadResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(typ, nil)}}
	(&SingleDataSource{api: api}).Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: tftypes.NewValue(typ, vals)}}, &resp)
	return resp
}

func catalog() *fakeAPI {
	return &fakeAPI{images: []client.Image{
		{ID: "img_1", Slug: "ubuntu-24-04", Name: "Ubuntu", Version: "24.04 LTS", Zones: []string{"af-abj-1", "af-abj-2"}},
		{ID: "img_2", Slug: "debian-12", Name: "Debian", Version: "12"},
	}}
}

func TestSingleSchemaIsValid(t *testing.T) {
	var resp datasource.SchemaResponse
	NewSingle().Schema(ctx, datasource.SchemaRequest{}, &resp)
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	if !resp.Schema.Attributes["slug"].IsRequired() {
		t.Error("slug must be required")
	}
}

func TestSingleReadFindsTheImageBySlug(t *testing.T) {
	resp := readOne(t, catalog(), "ubuntu-24-04")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got singleModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if got.ID.ValueString() != "img_1" || got.Name.ValueString() != "Ubuntu" || got.Version.ValueString() != "24.04 LTS" ||
		len(got.Zones) != 2 || got.Zones[1].ValueString() != "af-abj-2" {
		t.Errorf("model = %+v", got)
	}
}

// An image with no zones must give an empty list, not null, like the list data source.
func TestSingleReadGivesAnEmptyZoneListNotNull(t *testing.T) {
	resp := readOne(t, catalog(), "debian-12")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var zones []string
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("zones"), &zones)...)
	if resp.Diagnostics.HasError() || zones == nil || len(zones) != 0 {
		t.Errorf("zones = %#v, diags %v", zones, resp.Diagnostics)
	}
}

func TestSingleReadReportsAMissingSlugWithWhatExists(t *testing.T) {
	resp := readOne(t, catalog(), "ubuntu")
	if !resp.Diagnostics.HasError() {
		t.Fatal("a missing slug must be an error")
	}
	e := resp.Diagnostics.Errors()[0]
	if !strings.Contains(e.Detail(), `"ubuntu"`) || !strings.Contains(e.Detail(), "debian-12, ubuntu-24-04") {
		t.Errorf("detail = %q", e.Detail())
	}
	if d, ok := e.(interface{ Path() path.Path }); !ok || d.Path().String() != "slug" {
		t.Errorf("the error should point at slug, got %v", e)
	}
}

func TestSingleReadPassesAPIErrorsThrough(t *testing.T) {
	resp := readOne(t, &fakeAPI{err: client.ErrNoRoute}, "ubuntu-24-04")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Error reading images") {
		t.Errorf("diagnostics = %v", resp.Diagnostics)
	}
}
