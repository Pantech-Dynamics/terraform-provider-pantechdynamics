package cloudproviders

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	list []client.CloudProvider
	err  error
}

func (f *fakeAPI) ListCloudProviders(context.Context) ([]client.CloudProvider, error) {
	return f.list, f.err
}

func read(t *testing.T, api providersAPI) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	New().Schema(ctx, datasource.SchemaRequest{}, &sresp)
	if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	typ, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	cfg := tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&DataSource{api: api}).Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	return resp
}

func TestReadListsTheProviders(t *testing.T) {
	resp := read(t, &fakeAPI{list: []client.CloudProvider{
		{Slug: "aws", Name: "AWS", Access: client.CloudAccessFederated, CostReadable: true, PeopleReadable: true, Available: true},
		{Slug: "hetzner", Name: "Hetzner", Access: client.CloudAccessAgent},
	}})
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(m.Providers) != 2 || !m.Providers[0].CostReadable.ValueBool() || m.Providers[1].Access.ValueString() != "agent" || m.Providers[1].Available.ValueBool() {
		t.Fatalf("model = %+v", m)
	}
}

func TestReadEmptyIsAnEmptyList(t *testing.T) {
	resp := read(t, &fakeAPI{})
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Providers == nil || len(m.Providers) != 0 {
		t.Fatalf("providers = %v", m.Providers)
	}
}

func TestReadUnavailableExplainsIt(t *testing.T) {
	resp := read(t, &fakeAPI{err: &client.APIError{Status: 503, Code: client.CodeMigrateUnavailable}})
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not available on this platform") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}
