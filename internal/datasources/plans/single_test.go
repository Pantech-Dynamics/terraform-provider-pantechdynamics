package plans

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func readOne(t *testing.T, api planAPI, slug string, placement tftypes.Value) datasource.ReadResponse {
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
	vals["placement"] = placement

	resp := datasource.ReadResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(typ, nil)}}
	(&SingleDataSource{api: api}).Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: tftypes.NewValue(typ, vals)}}, &resp)
	return resp
}

var noPlacement = tftypes.NewValue(tftypes.String, nil)

func catalog() []client.Plan {
	reason := "NO_PRICE_FOR_CURRENCY"
	return []client.Plan{
		{ID: "plan_1", Slug: "individual", Name: "Individual", VCPU: 1, MemoryMB: 1024, DiskGB: 20,
			Price: &client.PlanPrice{Currency: "NGN", MonthlyEstimateMinor: 3760000, StorageFloorMinor: 460000, InitialPaymentMinor: 1504000}},
		{ID: "plan_2", Slug: "starter", Name: "Starter", VCPU: 1, MemoryMB: 2048, DiskGB: 40, UnpricedReason: &reason},
	}
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

func TestSingleReadFindsThePlanBySlug(t *testing.T) {
	api := &fakeAPI{plans: catalog()}
	resp := readOne(t, api, "individual", tftypes.NewValue(tftypes.String, "vpc"))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.placement != "vpc" {
		t.Errorf("placement sent = %q, want vpc", api.placement)
	}
	var got singleModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if got.ID.ValueString() != "plan_1" || got.Slug.ValueString() != "individual" || got.VCPU.ValueInt64() != 1 ||
		got.InitialPaymentMinor.ValueInt64() != 1504000 || got.PriceCurrency.ValueString() != "NGN" || got.Placement.ValueString() != "vpc" {
		t.Errorf("model = %+v", got)
	}
}

func TestSingleReadLeavesAnUnpricedPlanWithNullPrices(t *testing.T) {
	resp := readOne(t, &fakeAPI{plans: catalog()}, "starter", noPlacement)
	var got singleModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !got.PriceCurrency.IsNull() || !got.InitialPaymentMinor.IsNull() || got.UnpricedReason.ValueString() != "NO_PRICE_FOR_CURRENCY" {
		t.Errorf("model = %+v", got)
	}
	if !got.Placement.IsNull() {
		t.Errorf("placement = %v, an unset placement must stay null", got.Placement)
	}
}

func TestSingleReadReportsAMissingSlugWithWhatExists(t *testing.T) {
	resp := readOne(t, &fakeAPI{plans: catalog()}, "indivdual", noPlacement)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a missing slug must be an error")
	}
	e := resp.Diagnostics.Errors()[0]
	if !strings.Contains(e.Detail(), `"indivdual"`) || !strings.Contains(e.Detail(), "individual, starter") || !strings.Contains(e.Detail(), "standard placement") {
		t.Errorf("detail = %q", e.Detail())
	}
	if d, ok := e.(interface{ Path() path.Path }); !ok || d.Path().String() != "slug" {
		t.Errorf("the error should point at slug, got %v", e)
	}
}

func TestSingleReadMatchesExactly(t *testing.T) {
	resp := readOne(t, &fakeAPI{plans: catalog()}, "Individual", noPlacement)
	if !resp.Diagnostics.HasError() {
		t.Error("the match is case-sensitive: Individual is not individual")
	}
}

func TestSingleReadPassesAPIErrorsThrough(t *testing.T) {
	resp := readOne(t, &fakeAPI{err: client.ErrNoRoute}, "individual", noPlacement)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Error reading plans") {
		t.Errorf("diagnostics = %v", resp.Diagnostics)
	}
}
