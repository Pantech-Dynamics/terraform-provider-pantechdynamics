package dynamicip

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	items map[string]client.DynamicIP
	err   error
}

func (f *fakeAPI) GetDynamicIP(_ context.Context, instanceID string) (*client.DynamicIP, error) {
	if f.err != nil {
		return nil, f.err
	}
	if item, ok := f.items[instanceID]; ok {
		return &item, nil
	}
	return nil, fmt.Errorf("getting Dynamic IP: %w", &client.APIError{Status: 404, Code: client.CodeAddressRotationNotFound, RequestID: "req_1"})
}

func ptr(s string) *string { return &s }

func stamp(h int) *time.Time {
	t := time.Date(2026, 11, 24, h, 0, 0, 0, time.UTC)
	return &t
}

func read(t *testing.T, ds datasource.DataSource, instanceID string) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &sresp)
	if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	typ, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("schema is not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["instance_id"] = tftypes.NewValue(tftypes.String, instanceID)
	cfg := tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	ds.Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	return resp
}

func TestMetadataAndConfigure(t *testing.T) {
	var meta datasource.MetadataResponse
	New().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &meta)
	if meta.TypeName != "pantechdynamics_dynamic_ip" {
		t.Fatalf("TypeName = %q", meta.TypeName)
	}

	var bad datasource.ConfigureResponse
	(&DataSource{}).Configure(ctx, datasource.ConfigureRequest{ProviderData: 123}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Fatal("wrong provider data must error")
	}

	c, err := client.New("https://api.example.com/public/v1", "PAN_x", "test")
	if err != nil {
		t.Fatal(err)
	}
	ds := &DataSource{}
	var ok datasource.ConfigureResponse
	ds.Configure(ctx, datasource.ConfigureRequest{ProviderData: c}, &ok)
	if ok.Diagnostics.HasError() || ds.api == nil {
		t.Fatalf("real client must satisfy dynamicIPAPI: %v", ok.Diagnostics)
	}
}

func TestRead(t *testing.T) {
	t.Run("returns full state including events", func(t *testing.T) {
		api := &fakeAPI{items: map[string]client.DynamicIP{
			"vm_1": {
				InstanceID:       "vm_1",
				Schedule:         client.DynamicIPScheduleDaily,
				Connections:      client.DynamicIPConnectionsKeep,
				OnDemand:         true,
				State:            client.DynamicIPStateActive,
				CurrentAddress:   ptr("102.211.122.90"),
				NextRotationAt:   stamp(18),
				LastRotatedAt:    stamp(6),
				SurchargePercent: 50,
				OnDemandUsed:     1,
				OnDemandLimit:    10,
				Events: []client.DynamicIPEvent{
					{Kind: "on_demand", FromAddress: ptr("102.211.122.89"), ToAddress: ptr("102.211.122.90"), Degraded: false, OccurredAt: stamp(6)},
					{Kind: "enabled", ToAddress: ptr("102.211.122.89"), Degraded: false, OccurredAt: stamp(1)},
				},
			},
		}}

		resp := read(t, &DataSource{api: api}, "vm_1")
		if resp.Diagnostics.HasError() {
			t.Fatalf("diagnostics = %v", resp.Diagnostics)
		}
		var m model
		if d := resp.State.Get(ctx, &m); d.HasError() {
			t.Fatalf("state.Get = %v", d)
		}
		if m.ID.ValueString() != "vm_1" || m.Schedule.ValueString() != "daily" || m.CurrentAddress.ValueString() != "102.211.122.90" ||
			m.SurchargePercent.ValueFloat64() != 50 || m.OnDemandUsed.ValueInt64() != 1 || m.OnDemandLimit.ValueInt64() != 10 {
			t.Fatalf("model = %+v", m)
		}
		if len(m.Events) != 2 || m.Events[0].Kind.ValueString() != "on_demand" || m.Events[0].FromAddress.ValueString() != "102.211.122.89" ||
			!m.Events[1].FromAddress.IsNull() || m.Events[1].ToAddress.ValueString() != "102.211.122.89" {
			t.Fatalf("events = %+v", m.Events)
		}
	})

	t.Run("not found gives hint", func(t *testing.T) {
		resp := read(t, &DataSource{api: &fakeAPI{items: map[string]client.DynamicIP{}}}, "vm_9")
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error")
		}
		detail := resp.Diagnostics.Errors()[0].Detail()
		if !strings.Contains(detail, "Dynamic IP is not enabled") || !strings.Contains(detail, "req_1") {
			t.Fatalf("detail = %q", detail)
		}
	})
}
