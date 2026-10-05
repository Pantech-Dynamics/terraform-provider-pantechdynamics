package databaseengines

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	engines []client.DatabaseEngine
	err     error
}

func (f *fakeAPI) ListDatabaseEngines(context.Context) ([]client.DatabaseEngine, error) {
	return f.engines, f.err
}

func read(t *testing.T, api engineAPI) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	New().Schema(ctx, datasource.SchemaRequest{}, &sresp)
	if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	typ := sresp.Schema.Type().TerraformType(ctx)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&DataSource{api: api}).Read(ctx, datasource.ReadRequest{}, &resp)
	return resp
}

func TestRead(t *testing.T) {
	eol := "2030-11-14"
	api := &fakeAPI{engines: []client.DatabaseEngine{
		{Engine: "postgresql", DisplayName: "PostgreSQL", Port: 5432, Versions: []client.DatabaseEngineVersion{{Version: "18", EOLDate: &eol, Zones: []string{"af-abj-1", "af-abj-2"}}}},
		{Engine: "mariadb", DisplayName: "MariaDB", Port: 3306},
	}}
	resp := read(t, api)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if len(m.Engines) != 2 || m.Engines[0].Port.ValueInt64() != 5432 || m.Engines[0].Versions[0].EOLDate.ValueString() != eol || len(m.Engines[0].Versions[0].Zones) != 2 {
		t.Fatalf("model = %+v", m)
	}
	if m.Engines[1].Versions == nil || len(m.Engines[1].Versions) != 0 {
		t.Errorf("an engine with no versions must show an empty list, got %v", m.Engines[1].Versions)
	}
}

func TestReadError(t *testing.T) {
	if resp := read(t, &fakeAPI{err: errors.New("boom")}); !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
}
