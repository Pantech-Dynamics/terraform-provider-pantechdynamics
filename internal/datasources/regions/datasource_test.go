package regions

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
	regions []client.Region
	err     error
}

func (f *fakeAPI) ListRegions(context.Context) ([]client.Region, error) { return f.regions, f.err }

func read(t *testing.T, api regionAPI) datasource.ReadResponse {
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
	reason := "ACCOUNT_NOT_READY"
	api := &fakeAPI{regions: []client.Region{
		{Code: "af-abj", Name: "Abuja", Placements: []client.Placement{
			{Kind: "standard", Zone: "af-abj-1", Available: true},
			{Kind: "vpc", Zone: "af-abj-2", Available: false, UnavailableReason: &reason},
		}},
		{Code: "eu-test", Name: "Nowhere"},
	}}

	resp := read(t, api)

	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if got.ID.ValueString() != "regions" || len(got.Regions) != 2 {
		t.Fatalf("state = %+v", got)
	}
	abuja := got.Regions[0]
	if abuja.Code.ValueString() != "af-abj" || len(abuja.Placements) != 2 {
		t.Fatalf("abuja = %+v", abuja)
	}
	std, vpc := abuja.Placements[0], abuja.Placements[1]
	if std.Kind.ValueString() != "standard" || !std.Available.ValueBool() || !std.UnavailableReason.IsNull() {
		t.Fatalf("standard = %+v", std)
	}
	if vpc.Available.ValueBool() || vpc.UnavailableReason.ValueString() != reason {
		t.Fatalf("vpc = %+v", vpc)
	}
	if got.Regions[1].Placements == nil || len(got.Regions[1].Placements) != 0 {
		t.Fatalf("a region with no placements must have an empty list, got %#v", got.Regions[1].Placements)
	}
}

func TestReadEmptyAndError(t *testing.T) {
	resp := read(t, &fakeAPI{})
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if resp.Diagnostics.HasError() || got.Regions == nil || len(got.Regions) != 0 {
		t.Fatalf("diags = %v, regions = %#v", resp.Diagnostics, got.Regions)
	}
	if !read(t, &fakeAPI{err: &client.APIError{Status: 403, Code: "FORBIDDEN"}}).Diagnostics.HasError() {
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
