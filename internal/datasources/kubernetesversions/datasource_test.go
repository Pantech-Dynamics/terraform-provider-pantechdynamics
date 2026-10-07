package kubernetesversions

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
	list     *client.KubernetesVersionList
	err      error
	lastZone string
}

func (f *fakeAPI) ListKubernetesVersions(_ context.Context, zoneID string) (*client.KubernetesVersionList, error) {
	f.lastZone = zoneID
	return f.list, f.err
}

func read(t *testing.T, api versionAPI, zoneID *string) datasource.ReadResponse {
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
	if zoneID != nil {
		vals["zone_id"] = tftypes.NewValue(tftypes.String, *zoneID)
	}
	cfg := tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&DataSource{api: api}).Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	return resp
}

func TestReadFiltersByZone(t *testing.T) {
	zone := "af-abj-2"
	api := &fakeAPI{list: &client.KubernetesVersionList{
		Data: []client.KubernetesVersion{
			{ID: "k8sv_2", ZoneID: zone, Version: "1.32.0", Status: "available", MinCPU: 2, MinMemoryMB: 2048},
			{ID: "k8sv_1", ZoneID: zone, Version: "1.31.2", Status: "withdrawn", MinCPU: 2, MinMemoryMB: 2048},
		},
		HAZoneIDs: []string{zone},
	}}
	resp := read(t, api, &zone)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if api.lastZone != zone || m.ID.ValueString() != zone || len(m.Versions) != 2 || m.Versions[0].ID.ValueString() != "k8sv_2" ||
		m.Versions[1].Status.ValueString() != "withdrawn" || m.Versions[0].MinMemoryMB.ValueInt64() != 2048 || len(m.HAZoneIDs) != 1 {
		t.Errorf("model = %+v", m)
	}
}

func TestReadEverywhereShowsEmptyLists(t *testing.T) {
	api := &fakeAPI{list: &client.KubernetesVersionList{}}
	resp := read(t, api, nil)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if api.lastZone != "" || m.ID.ValueString() != "kubernetes_versions" || m.Versions == nil || m.HAZoneIDs == nil {
		t.Errorf("model = %+v, want empty, non-null lists", m)
	}
}

func TestReadUnavailableExplainsIt(t *testing.T) {
	api := &fakeAPI{err: &client.APIError{Status: 503, Code: client.CodeKubernetesUnavailable}}
	resp := read(t, api, nil)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not open on this platform yet") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}
