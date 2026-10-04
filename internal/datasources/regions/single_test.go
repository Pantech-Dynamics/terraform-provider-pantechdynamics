package regions

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func readOne(t *testing.T, api regionAPI, code string) datasource.ReadResponse {
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
	vals["code"] = tftypes.NewValue(tftypes.String, code)

	resp := datasource.ReadResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(typ, nil)}}
	(&SingleDataSource{api: api}).Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: tftypes.NewValue(typ, vals)}}, &resp)
	return resp
}

func catalog() *fakeAPI {
	reason := "IP_POOL_EXHAUSTED"
	return &fakeAPI{regions: []client.Region{
		{Code: "af-abj", Name: "Abuja", Placements: []client.Placement{
			{Kind: "standard", Zone: "af-abj-1", Available: true},
			{Kind: "vpc", Zone: "af-abj-2", Available: false, UnavailableReason: &reason},
		}},
		{Code: "af-los", Name: "Lagos"},
	}}
}

func TestSingleSchemaIsValid(t *testing.T) {
	var resp datasource.SchemaResponse
	NewSingle().Schema(ctx, datasource.SchemaRequest{}, &resp)
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	if !resp.Schema.Attributes["code"].IsRequired() {
		t.Error("code must be required")
	}
}

func TestSingleReadFindsTheRegionByCode(t *testing.T) {
	resp := readOne(t, catalog(), "af-abj")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got singleModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if got.ID.ValueString() != "af-abj" || got.Name.ValueString() != "Abuja" || len(got.Placements) != 2 {
		t.Fatalf("model = %+v", got)
	}
	if p := got.Placements[0]; p.Kind.ValueString() != "standard" || !p.Available.ValueBool() || !p.UnavailableReason.IsNull() {
		t.Errorf("placement 0 = %+v", p)
	}
	if p := got.Placements[1]; p.Available.ValueBool() || p.UnavailableReason.ValueString() != "IP_POOL_EXHAUSTED" {
		t.Errorf("placement 1 = %+v", p)
	}
}

func TestSingleReadReportsAMissingCodeWithWhatExists(t *testing.T) {
	resp := readOne(t, catalog(), "af-nowhere")
	if !resp.Diagnostics.HasError() {
		t.Fatal("a missing code must be an error")
	}
	e := resp.Diagnostics.Errors()[0]
	if !strings.Contains(e.Detail(), `"af-nowhere"`) || !strings.Contains(e.Detail(), "af-abj, af-los") {
		t.Errorf("detail = %q", e.Detail())
	}
	if d, ok := e.(interface{ Path() path.Path }); !ok || d.Path().String() != "code" {
		t.Errorf("the error should point at code, got %v", e)
	}
}

func TestSingleReadPassesAPIErrorsThrough(t *testing.T) {
	resp := readOne(t, &fakeAPI{err: client.ErrNoRoute}, "af-abj")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Error reading regions") {
		t.Errorf("diagnostics = %v", resp.Diagnostics)
	}
}
