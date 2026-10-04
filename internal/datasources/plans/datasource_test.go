package plans

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	plans     []client.Plan
	err       error
	placement string
	calls     int
}

func (f *fakeAPI) ListPlans(_ context.Context, placement string) ([]client.Plan, error) {
	f.calls++
	f.placement = placement
	return f.plans, f.err
}

func readWith(t *testing.T, api planAPI, placement tftypes.Value) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	New().Schema(ctx, datasource.SchemaRequest{}, &sresp)
	s := sresp.Schema
	typ, ok := s.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("the schema type is not an object")
	}

	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["placement"] = placement

	resp := datasource.ReadResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(typ, nil)}}
	(&DataSource{api: api}).Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: tftypes.NewValue(typ, vals)}}, &resp)
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
	reason := "NO_PRICE_FOR_CURRENCY"
	api := &fakeAPI{plans: []client.Plan{
		{ID: "plan_1", Slug: "individual", Name: "Individual", VCPU: 1, MemoryMB: 1024, DiskGB: 20,
			Price: &client.PlanPrice{Currency: "NGN", MonthlyEstimateMinor: 3760000, StorageFloorMinor: 460000, InitialPaymentMinor: 1504000}},
		{ID: "plan_2", Slug: "starter", Name: "Starter", VCPU: 1, MemoryMB: 2048, DiskGB: 40, UnpricedReason: &reason},
	}}

	resp := readWith(t, api, tftypes.NewValue(tftypes.String, "vpc"))

	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.placement != "vpc" {
		t.Fatalf("placement sent = %q", api.placement)
	}
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if got.ID.ValueString() != "plans" || got.Placement.ValueString() != "vpc" || len(got.Plans) != 2 {
		t.Fatalf("state = %+v", got)
	}
	priced, unpriced := got.Plans[0], got.Plans[1]
	if priced.Slug.ValueString() != "individual" || priced.MonthlyEstimateMinor.ValueInt64() != 3760000 || priced.PriceCurrency.ValueString() != "NGN" {
		t.Fatalf("priced = %+v", priced)
	}
	if !unpriced.MonthlyEstimateMinor.IsNull() || !unpriced.PriceCurrency.IsNull() || unpriced.UnpricedReason.ValueString() != reason {
		t.Fatalf("unpriced plan must have null prices and a reason, got %+v", unpriced)
	}
}

func TestReadWithoutPlacementUsesBackendDefault(t *testing.T) {
	api := &fakeAPI{}
	resp := readWith(t, api, tftypes.NewValue(tftypes.String, nil))
	if resp.Diagnostics.HasError() || api.placement != "" {
		t.Fatalf("diags = %v, placement = %q", resp.Diagnostics, api.placement)
	}
}

func TestReadEmptyIsAnEmptyList(t *testing.T) {
	resp := readWith(t, &fakeAPI{}, tftypes.NewValue(tftypes.String, nil))
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if resp.Diagnostics.HasError() || got.Plans == nil || len(got.Plans) != 0 {
		t.Fatalf("diags = %v, plans = %#v", resp.Diagnostics, got.Plans)
	}
}

func TestReadAPIErrorIsReported(t *testing.T) {
	api := &fakeAPI{err: &client.APIError{Status: 401, Code: "UNAUTHENTICATED", RequestID: "req_5"}}
	resp := readWith(t, api, tftypes.NewValue(tftypes.String, nil))
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "req_5") {
		t.Fatalf("diags = %v", resp.Diagnostics)
	}
}

func TestPlacementValidator(t *testing.T) {
	v := oneOf{allowed: []string{"standard", "vpc"}}
	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{"standard", types.StringValue("standard"), false},
		{"vpc", types.StringValue("vpc"), false},
		{"bogus", types.StringValue("bogus"), true},
		{"empty", types.StringValue(""), true},
		{"null is allowed", types.StringNull(), false},
		{"unknown is allowed", types.StringUnknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{ConfigValue: tt.value}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %v", resp.Diagnostics)
			}
		})
	}
}

func TestConfigureWrongType(t *testing.T) {
	var resp datasource.ConfigureResponse
	(&DataSource{}).Configure(ctx, datasource.ConfigureRequest{ProviderData: 42}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	var nilResp datasource.ConfigureResponse
	(&DataSource{}).Configure(ctx, datasource.ConfigureRequest{}, &nilResp)
	if nilResp.Diagnostics.HasError() {
		t.Fatal("nil provider data must be ignored")
	}
}
