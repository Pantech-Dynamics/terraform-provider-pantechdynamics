package snapshotschedule

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

// fakeAPI keeps one schedule per source id, as the platform does. A source with no
// schedule answers 404 SNAPSHOT_SCHEDULE_NOT_FOUND, and the call is an upsert.
type fakeAPI struct {
	schedules map[string]client.SnapshotSchedule
	putErr    error
	getErr    error
	puts      []client.PutSnapshotScheduleRequest
}

func newFake() *fakeAPI { return &fakeAPI{schedules: map[string]client.SnapshotSchedule{}} }

func ptr(s string) *string { return &s }

func (f *fakeAPI) get(id string) (*client.SnapshotSchedule, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if s, ok := f.schedules[id]; ok {
		return &s, nil
	}
	return nil, fmt.Errorf("getting snapshot schedule: %w", &client.APIError{Status: 404, Code: "SNAPSHOT_SCHEDULE_NOT_FOUND"})
}

func (f *fakeAPI) put(id string, isInstance bool, req client.PutSnapshotScheduleRequest) (*client.SnapshotSchedule, error) {
	f.puts = append(f.puts, req)
	if f.putErr != nil {
		return nil, f.putErr
	}
	next := time.Date(2026, 10, 4, 13, 27, 30, 0, time.UTC)
	s := client.SnapshotSchedule{Frequency: req.Frequency, RetentionCount: req.RetentionCount, Enabled: req.Enabled, NextRunAt: &next}
	if isInstance {
		s.InstanceID = ptr(id)
	} else {
		s.VolumeID = ptr(id)
	}
	f.schedules[id] = s
	return &s, nil
}

func (f *fakeAPI) GetInstanceSnapshotSchedule(_ context.Context, id string) (*client.SnapshotSchedule, error) {
	return f.get(id)
}

func (f *fakeAPI) GetVolumeSnapshotSchedule(_ context.Context, id string) (*client.SnapshotSchedule, error) {
	return f.get(id)
}

func (f *fakeAPI) PutInstanceSnapshotSchedule(_ context.Context, id string, req client.PutSnapshotScheduleRequest) (*client.SnapshotSchedule, error) {
	return f.put(id, true, req)
}

func (f *fakeAPI) PutVolumeSnapshotSchedule(_ context.Context, id string, req client.PutSnapshotScheduleRequest) (*client.SnapshotSchedule, error) {
	return f.put(id, false, req)
}

// --- helpers ---

func testSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	New().Schema(ctx, resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func objectType(s schema.Schema) tftypes.Object {
	obj, ok := s.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		panic("the schema type is not an object")
	}
	return obj
}

func values(s schema.Schema, set map[string]tftypes.Value) tftypes.Value {
	typ := objectType(s)
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tftypes.NewValue(typ, vals)
}

func str(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

func num(v int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }

func flag(v bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, v) }

func unknownStr() tftypes.Value { return tftypes.NewValue(tftypes.String, tftypes.UnknownValue) }

func emptyState(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(objectType(s), nil)}
}

// plan builds a plan. The platform defaults (7 and true) are filled in by Terraform
// before the resource sees the plan, so they are set here too.
func plan(s schema.Schema, over map[string]tftypes.Value) tfsdk.Plan {
	set := map[string]tftypes.Value{
		"id": unknownStr(), "instance_id": str("vm_1"), "frequency": str("daily"),
		"retention_count": num(7), "enabled": flag(true), "next_run_at": unknownStr(),
	}
	for k, v := range over {
		set[k] = v
	}
	return tfsdk.Plan{Schema: s, Raw: values(s, set)}
}

// stored is the saved state of the weekly schedule, keeping 5, of instance vm_1.
func stored(s schema.Schema) tfsdk.State {
	set := map[string]tftypes.Value{
		"id": str("vm_1"), "instance_id": str("vm_1"), "frequency": str("weekly"),
		"retention_count": num(5), "enabled": flag(true), "next_run_at": str("2026-10-04T13:27:30Z"),
	}
	return tfsdk.State{Schema: s, Raw: values(s, set)}
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func errorText(d diag.Diagnostics) string {
	var b strings.Builder
	for _, e := range d.Errors() {
		b.WriteString(e.Summary() + " " + e.Detail() + "\n")
	}
	return b.String()
}

func newTestResource(api scheduleAPI) *Resource { return &Resource{api: api} }

// --- tests ---

func TestSchemaIsValid(t *testing.T) {
	if d := testSchema(t).ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func TestMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	New().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &resp)
	if resp.TypeName != "pantechdynamics_snapshot_schedule" {
		t.Fatalf("TypeName = %q", resp.TypeName)
	}
}

func TestConfigure(t *testing.T) {
	var bad resource.ConfigureResponse
	(&Resource{}).Configure(ctx, resource.ConfigureRequest{ProviderData: 42}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Fatal("wrong type must error")
	}
	var none resource.ConfigureResponse
	(&Resource{}).Configure(ctx, resource.ConfigureRequest{}, &none)
	if none.Diagnostics.HasError() {
		t.Fatal("nil provider data must be ignored")
	}
	c, err := client.New("https://api.example.com/v1", "PAN_x", "test")
	if err != nil {
		t.Fatal(err)
	}
	r := &Resource{}
	var ok resource.ConfigureResponse
	r.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &ok)
	if ok.Diagnostics.HasError() || r.api == nil {
		t.Fatalf("a real client must satisfy scheduleAPI: %v", ok.Diagnostics)
	}
}

func TestValidateConfigNeedsExactlyOneSource(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
	}{
		{"an instance", map[string]tftypes.Value{"instance_id": str("vm_1")}, false},
		{"a volume", map[string]tftypes.Value{"volume_id": str("vol_1")}, false},
		{"neither", nil, true},
		{"both", map[string]tftypes.Value{"instance_id": str("vm_1"), "volume_id": str("vol_1")}, true},
		{"unknown counts as set", map[string]tftypes.Value{"volume_id": unknownStr()}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := map[string]tftypes.Value{"frequency": str("daily")}
			for k, v := range tt.set {
				base[k] = v
			}
			var resp resource.ValidateConfigResponse
			(&Resource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: values(s, base)}}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s", errorText(resp.Diagnostics))
			}
		})
	}
}

func TestValidators(t *testing.T) {
	t.Run("frequency", func(t *testing.T) {
		v := oneOf{allowed: []string{"daily", "weekly", "monthly"}}
		for value, wantErr := range map[string]bool{"daily": false, "weekly": false, "monthly": false, "hourly": true, "": true, "Daily": true} {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{ConfigValue: types.StringValue(value)}, &resp)
			if resp.Diagnostics.HasError() != wantErr {
				t.Errorf("%q: diags = %v", value, resp.Diagnostics)
			}
		}
	})
	t.Run("retention count", func(t *testing.T) {
		for n, wantErr := range map[int64]bool{1: false, 7: false, 168: false, 0: true, 169: true, -1: true} {
			var resp validator.Int64Response
			retentionRange{}.ValidateInt64(ctx, validator.Int64Request{ConfigValue: types.Int64Value(n)}, &resp)
			if resp.Diagnostics.HasError() != wantErr {
				t.Errorf("%d: diags = %v", n, resp.Diagnostics)
			}
		}
		var resp validator.Int64Response
		retentionRange{}.ValidateInt64(ctx, validator.Int64Request{ConfigValue: types.Int64Unknown()}, &resp)
		if resp.Diagnostics.HasError() {
			t.Error("unknown must be skipped")
		}
	})
	t.Run("source ids", func(t *testing.T) {
		v := prefixCheck{prefix: "vm_", kind: "an instance"}
		for value, wantErr := range map[string]bool{"vm_1": false, "vol_1": true, "": true} {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{ConfigValue: types.StringValue(value)}, &resp)
			if resp.Diagnostics.HasError() != wantErr {
				t.Errorf("%q: diags = %v", value, resp.Diagnostics)
			}
		}
	})
}

func TestCreate(t *testing.T) {
	s := testSchema(t)
	create := func(api *fakeAPI, over map[string]tftypes.Value) resource.CreateResponse {
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: plan(s, over)}, &resp)
		return resp
	}

	t.Run("an instance schedule sends every field", func(t *testing.T) {
		api := newFake()
		resp := create(api, nil)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		want := client.PutSnapshotScheduleRequest{Frequency: "daily", RetentionCount: 7, Enabled: true}
		if len(api.puts) != 1 || api.puts[0] != want {
			t.Fatalf("puts = %+v, want %+v", api.puts, want)
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "vm_1" || m.InstanceID.ValueString() != "vm_1" || !m.VolumeID.IsNull() || m.NextRunAt.ValueString() != "2026-10-04T13:27:30Z" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("a volume schedule", func(t *testing.T) {
		api := newFake()
		resp := create(api, map[string]tftypes.Value{"instance_id": tftypes.NewValue(tftypes.String, nil), "volume_id": str("vol_1"), "frequency": str("monthly"), "retention_count": num(2), "enabled": flag(false)})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "vol_1" || m.VolumeID.ValueString() != "vol_1" || m.Enabled.ValueBool() || m.Frequency.ValueString() != "monthly" {
			t.Fatalf("state = %+v", m)
		}
		if api.puts[0].Enabled {
			t.Fatal("a false value must be sent, not omitted")
		}
	})

	t.Run("a rejected schedule reports the platform's text", func(t *testing.T) {
		api := newFake()
		api.putErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Detail: "frequency must be daily, weekly or monthly, and retention_count 1 to 168."}
		resp := create(api, nil)
		if !strings.Contains(errorText(resp.Diagnostics), "retention_count 1 to 168") || !resp.State.Raw.IsNull() {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
}

func TestUpdate(t *testing.T) {
	s := testSchema(t)
	api := newFake()
	api.schedules["vm_1"] = client.SnapshotSchedule{InstanceID: ptr("vm_1"), Frequency: "weekly", RetentionCount: 5, Enabled: true}
	resp := resource.UpdateResponse{State: stored(s)}

	newTestResource(api).Update(ctx, resource.UpdateRequest{Plan: plan(s, map[string]tftypes.Value{"id": str("vm_1"), "frequency": str("daily"), "retention_count": num(3)}), State: stored(s)}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatal(errorText(resp.Diagnostics))
	}
	m := getModel(t, resp.State)
	if len(api.puts) != 1 || m.Frequency.ValueString() != "daily" || m.RetentionCount.ValueInt64() != 3 {
		t.Fatalf("puts = %d, state = %+v", len(api.puts), m)
	}
}

func TestRead(t *testing.T) {
	s := testSchema(t)
	read := func(api *fakeAPI) resource.ReadResponse {
		resp := resource.ReadResponse{State: stored(s)}
		newTestResource(api).Read(ctx, resource.ReadRequest{State: stored(s)}, &resp)
		return resp
	}

	t.Run("a schedule paused outside Terraform shows as paused", func(t *testing.T) {
		api := newFake()
		api.schedules["vm_1"] = client.SnapshotSchedule{InstanceID: ptr("vm_1"), Frequency: "weekly", RetentionCount: 5, Enabled: false}
		resp := read(api)
		if resp.Diagnostics.HasError() || getModel(t, resp.State).Enabled.ValueBool() {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
	t.Run("a source with no schedule removes it from state", func(t *testing.T) {
		if resp := read(newFake()); resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
	t.Run("a server error is reported and state is kept", func(t *testing.T) {
		api := newFake()
		api.getErr = &client.APIError{Status: 500, Code: "INTERNAL", RequestID: "req_7"}
		resp := read(api)
		if !strings.Contains(errorText(resp.Diagnostics), "req_7") || resp.State.Raw.IsNull() {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
}

// The API has no way to delete a schedule, so destroying one pauses it.
func TestDeletePausesTheSchedule(t *testing.T) {
	s := testSchema(t)
	del := func(api *fakeAPI) resource.DeleteResponse {
		var resp resource.DeleteResponse
		newTestResource(api).Delete(ctx, resource.DeleteRequest{State: stored(s)}, &resp)
		return resp
	}

	t.Run("an enabled schedule is paused with its own frequency and retention", func(t *testing.T) {
		api := newFake()
		api.schedules["vm_1"] = client.SnapshotSchedule{InstanceID: ptr("vm_1"), Frequency: "monthly", RetentionCount: 12, Enabled: true}
		if resp := del(api); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		want := client.PutSnapshotScheduleRequest{Frequency: "monthly", RetentionCount: 12, Enabled: false}
		if len(api.puts) != 1 || api.puts[0] != want {
			t.Fatalf("puts = %+v, want %+v: only enabled changes, so the settings survive", api.puts, want)
		}
	})
	t.Run("an already paused schedule needs nothing", func(t *testing.T) {
		api := newFake()
		api.schedules["vm_1"] = client.SnapshotSchedule{InstanceID: ptr("vm_1"), Frequency: "weekly", RetentionCount: 5, Enabled: false}
		if resp := del(api); resp.Diagnostics.HasError() || len(api.puts) != 0 {
			t.Fatalf("puts = %d, diags = %s", len(api.puts), errorText(resp.Diagnostics))
		}
	})
	t.Run("a source that is gone needs nothing", func(t *testing.T) {
		api := newFake()
		if resp := del(api); resp.Diagnostics.HasError() || len(api.puts) != 0 {
			t.Fatalf("puts = %d, diags = %s", len(api.puts), errorText(resp.Diagnostics))
		}
	})
	t.Run("a failure to pause is reported", func(t *testing.T) {
		api := newFake()
		api.schedules["vm_1"] = client.SnapshotSchedule{InstanceID: ptr("vm_1"), Frequency: "weekly", RetentionCount: 5, Enabled: true}
		api.putErr = &client.APIError{Status: 500, Code: "INTERNAL"}
		if text := errorText(del(api).Diagnostics); !strings.Contains(text, "Error pausing") {
			t.Fatalf("diags = %s", text)
		}
	})
}

func TestImportState(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		name, id     string
		wantErr      bool
		wantInstance bool
	}{
		{"an instance", "vm_1", false, true},
		{"a volume", "vol_1", false, false},
		{"another kind of id", "snap_1", true, false},
		{"empty", "", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: values(s, nil)}}
			newTestResource(newFake()).ImportState(ctx, resource.ImportStateRequest{ID: tt.id}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s", errorText(resp.Diagnostics))
			}
			if tt.wantErr {
				return
			}
			m := getModel(t, resp.State)
			if m.ID.ValueString() != tt.id || m.InstanceID.IsNull() == tt.wantInstance || m.VolumeID.IsNull() != tt.wantInstance {
				t.Fatalf("state = %+v", m)
			}
		})
	}
}
