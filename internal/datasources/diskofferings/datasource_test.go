package diskofferings

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
	offerings []client.DiskOffering
	err       error
}

func (f *fakeAPI) ListDiskOfferings(context.Context) ([]client.DiskOffering, error) {
	return f.offerings, f.err
}

func read(t *testing.T, api offeringAPI) datasource.ReadResponse {
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
	five := int64(5)
	api := &fakeAPI{offerings: []client.DiskOffering{
		{Slug: "custom", Name: "Custom", CustomSize: true, StorageType: "shared", Currency: "NGN", HourlyPriceMinor: 32},
		{Slug: "small-5gb", Name: "Small", SizeGB: &five, StorageType: "shared", Currency: "NGN", HourlyPriceMinor: 160},
	}}

	resp := read(t, api)

	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if got.ID.ValueString() != "disk_offerings" || len(got.DiskOfferings) != 2 {
		t.Fatalf("state = %+v", got)
	}
	custom, fixed := got.DiskOfferings[0], got.DiskOfferings[1]
	if !custom.CustomSize.ValueBool() || !custom.SizeGB.IsNull() {
		t.Fatalf("custom = %+v: a customized offering has no size, so size_gb must be null, not zero", custom)
	}
	if fixed.Slug.ValueString() != "small-5gb" || fixed.SizeGB.ValueInt64() != 5 || fixed.StorageType.ValueString() != "shared" || fixed.HourlyPriceMinor.ValueInt64() != 160 {
		t.Fatalf("fixed = %+v", fixed)
	}
}

func TestReadEmptyAndError(t *testing.T) {
	resp := read(t, &fakeAPI{})
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if resp.Diagnostics.HasError() || got.DiskOfferings == nil || len(got.DiskOfferings) != 0 {
		t.Fatalf("diags = %v, offerings = %#v", resp.Diagnostics, got.DiskOfferings)
	}
	if !read(t, &fakeAPI{err: &client.APIError{Status: 500}}).Diagnostics.HasError() {
		t.Fatal("want an error")
	}
}

func TestConfigure(t *testing.T) {
	var bad datasource.ConfigureResponse
	(&DataSource{}).Configure(ctx, datasource.ConfigureRequest{ProviderData: 42}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Fatal("wrong type must error")
	}
	var none datasource.ConfigureResponse
	(&DataSource{}).Configure(ctx, datasource.ConfigureRequest{}, &none)
	if none.Diagnostics.HasError() {
		t.Fatal("nil provider data must be ignored")
	}
}
