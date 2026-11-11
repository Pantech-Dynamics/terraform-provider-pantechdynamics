package domaintlds

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
	list []client.TLDOffer
	err  error
}

func (f *fakeAPI) ListDomainTLDs(context.Context) ([]client.TLDOffer, error) { return f.list, f.err }

func read(t *testing.T, api tldAPI) datasource.ReadResponse {
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

func TestReadListsTheTLDs(t *testing.T) {
	resp := read(t, &fakeAPI{list: []client.TLDOffer{
		{TLD: "com", Currency: "USD", RegisterMinor: 1500, RenewMinor: 1600, TransferMinor: 1400, Featured: true},
		{TLD: "ng", Currency: "USD", RegisterMinor: 900, RenewMinor: 900, TransferMinor: 900},
	}})
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(m.TLDs) != 2 || m.TLDs[0].RenewMinor.ValueInt64() != 1600 || !m.TLDs[0].Featured.ValueBool() || m.TLDs[1].TLD.ValueString() != "ng" {
		t.Fatalf("model = %+v", m)
	}
}

func TestReadEmptyIsAnEmptyList(t *testing.T) {
	resp := read(t, &fakeAPI{})
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.TLDs == nil || len(m.TLDs) != 0 {
		t.Fatalf("tlds = %v", m.TLDs)
	}
}

func TestReadUnavailableExplainsIt(t *testing.T) {
	resp := read(t, &fakeAPI{err: &client.APIError{Status: 503, Code: client.CodeDomainsUnavailable}})
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not sold on this platform") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}
