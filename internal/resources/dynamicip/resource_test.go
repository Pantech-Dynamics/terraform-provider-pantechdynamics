package dynamicip

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

var ctx = context.Background()

type fakeAPI struct {
	items     map[string]client.DynamicIP
	getSeq    []client.DynamicIP
	getCalls  int
	puts      []client.PutDynamicIPRequest
	deletes   int
	putErr    error
	getErr    error
	deleteErr error
	waitErr   error
	putState  string
	delState  string
}

func newFake() *fakeAPI {
	return &fakeAPI{
		items:    map[string]client.DynamicIP{},
		putState: client.DynamicIPStateActive,
		delState: client.DynamicIPStateOff,
	}
}

func ptr(s string) *string { return &s }

func stamp(h int) *time.Time {
	t := time.Date(2026, 11, 24, h, 0, 0, 500000000, time.UTC)
	return &t
}

func (f *fakeAPI) GetDynamicIP(_ context.Context, instanceID string) (*client.DynamicIP, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if len(f.getSeq) > 0 {
		next := f.getSeq[0]
		f.getSeq = f.getSeq[1:]
		return &next, nil
	}
	if item, ok := f.items[instanceID]; ok {
		return &item, nil
	}
	return nil, fmt.Errorf("getting Dynamic IP: %w", &client.APIError{Status: 404, Code: client.CodeAddressRotationNotFound})
}

func (f *fakeAPI) PutDynamicIP(_ context.Context, instanceID string, req client.PutDynamicIPRequest) (*client.DynamicIP, error) {
	f.puts = append(f.puts, req)
	if f.putErr != nil {
		return nil, f.putErr
	}
	onDemand := req.OnDemand != nil && *req.OnDemand
	item := client.DynamicIP{
		InstanceID:       instanceID,
		Schedule:         req.Schedule,
		Connections:      req.Connections,
		OnDemand:         onDemand,
		State:            f.putState,
		SurchargePercent: 50,
		OnDemandUsed:     0,
		OnDemandLimit:    10,
	}
	if f.putState == client.DynamicIPStateActive {
		item.CurrentAddress = ptr("102.211.122.90")
		item.NextRotationAt = stamp(18)
		item.LastRotatedAt = stamp(6)
	}
	f.items[instanceID] = item
	return &item, nil
}

func (f *fakeAPI) DeleteDynamicIP(_ context.Context, instanceID string) (*client.DynamicIP, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	item, ok := f.items[instanceID]
	if !ok {
		return nil, fmt.Errorf("disabling Dynamic IP: %w", &client.APIError{Status: 404, Code: client.CodeAddressRotationNotFound})
	}
	item.State = f.delState
	if f.delState == client.DynamicIPStateOff {
		delete(f.items, instanceID)
	} else {
		f.items[instanceID] = item
	}
	return &item, nil
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.waitErr != nil {
		return f.waitErr
	}
	for i := 0; i < 10; i++ {
		ok, err := done(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("did not settle after 10 polls")
}

func testSchema(t *testing.T) schema.Schema {
	t.Helper()
	return kittest.SchemaOf(t, New())
}

func plan(s schema.Schema, over map[string]tftypes.Value) tfsdk.Plan {
	set := map[string]tftypes.Value{
		"id":                kittest.UnknownStr(),
		"instance_id":       kittest.Str("vm_1"),
		"schedule":          kittest.Str(client.DynamicIPScheduleDaily),
		"connections":       kittest.Str(client.DynamicIPConnectionsKeep),
		"on_demand":         tftypes.NewValue(tftypes.Bool, false),
		"state":             kittest.UnknownStr(),
		"current_address":   kittest.UnknownStr(),
		"next_rotation_at":  kittest.UnknownStr(),
		"last_rotated_at":   kittest.UnknownStr(),
		"surcharge_percent": kittest.Unknown(tftypes.Number),
		"on_demand_used":    kittest.Unknown(tftypes.Number),
		"on_demand_limit":   kittest.Unknown(tftypes.Number),
		"failure_reason":    kittest.UnknownStr(),
	}
	for k, v := range over {
		set[k] = v
	}
	return kittest.Plan(s, set)
}

func stored(s schema.Schema) tfsdk.State {
	return kittest.State(s, map[string]tftypes.Value{
		"id":                kittest.Str("vm_1"),
		"instance_id":       kittest.Str("vm_1"),
		"schedule":          kittest.Str(client.DynamicIPScheduleDaily),
		"connections":       kittest.Str(client.DynamicIPConnectionsKeep),
		"on_demand":         tftypes.NewValue(tftypes.Bool, false),
		"state":             kittest.Str(client.DynamicIPStateActive),
		"current_address":   kittest.Str("102.211.122.90"),
		"next_rotation_at":  kittest.Str("2026-11-24T18:00:00Z"),
		"last_rotated_at":   kittest.Str("2026-11-24T06:00:00Z"),
		"surcharge_percent": tftypes.NewValue(tftypes.Number, 50),
		"on_demand_used":    kittest.Num(0),
		"on_demand_limit":   kittest.Num(10),
	})
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func TestSchemaAndMetadata(t *testing.T) {
	if d := testSchema(t).ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	var meta resource.MetadataResponse
	New().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &meta)
	if meta.TypeName != "pantechdynamics_dynamic_ip" {
		t.Fatalf("TypeName = %q", meta.TypeName)
	}
}

func TestConfigure(t *testing.T) {
	var bad resource.ConfigureResponse
	(&Resource{}).Configure(ctx, resource.ConfigureRequest{ProviderData: 42}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Fatal("wrong provider data must error")
	}
	var none resource.ConfigureResponse
	(&Resource{}).Configure(ctx, resource.ConfigureRequest{}, &none)
	if none.Diagnostics.HasError() {
		t.Fatal("nil provider data must be ignored")
	}
	c, err := client.New("https://api.example.com/public/v1", "PAN_x", "test")
	if err != nil {
		t.Fatal(err)
	}
	r := &Resource{}
	var ok resource.ConfigureResponse
	r.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &ok)
	if ok.Diagnostics.HasError() || r.api == nil {
		t.Fatalf("real client must satisfy dynamicIPAPI: %v", ok.Diagnostics)
	}
}

func TestValidators(t *testing.T) {
	t.Run("instance_id", func(t *testing.T) {
		v := instanceIDValidator()
		for val, wantErr := range map[string]bool{"vm_1": false, "vol_1": true, "": true} {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{ConfigValue: types.StringValue(val)}, &resp)
			if resp.Diagnostics.HasError() != wantErr {
				t.Errorf("%q: diags = %v", val, resp.Diagnostics)
			}
		}
	})
	t.Run("schedule", func(t *testing.T) {
		v := scheduleValidator()
		for val, wantErr := range map[string]bool{
			"every_12_hours": false, "daily": false, "weekly": false, "monthly": false,
			"hourly": true, "": true,
		} {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{ConfigValue: types.StringValue(val)}, &resp)
			if resp.Diagnostics.HasError() != wantErr {
				t.Errorf("%q: diags = %v", val, resp.Diagnostics)
			}
		}
	})
	t.Run("connections", func(t *testing.T) {
		v := connectionsValidator()
		for val, wantErr := range map[string]bool{"keep": false, "move": false, "drop": true, "": true} {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{ConfigValue: types.StringValue(val)}, &resp)
			if resp.Diagnostics.HasError() != wantErr {
				t.Errorf("%q: diags = %v", val, resp.Diagnostics)
			}
		}
	})
}

func TestCreate(t *testing.T) {
	s := testSchema(t)

	t.Run("enables and polls until active", func(t *testing.T) {
		api := newFake()
		api.putState = client.DynamicIPStateEnabling
		api.getSeq = []client.DynamicIP{
			{InstanceID: "vm_1", Schedule: "daily", Connections: "keep", OnDemand: false, State: client.DynamicIPStateEnabling, SurchargePercent: 50, OnDemandLimit: 10},
			{InstanceID: "vm_1", Schedule: "daily", Connections: "keep", OnDemand: false, State: client.DynamicIPStateActive, CurrentAddress: ptr("102.211.122.90"), NextRotationAt: stamp(18), LastRotatedAt: stamp(6), SurchargePercent: 50, OnDemandLimit: 10},
		}
		resp := resource.CreateResponse{State: kittest.EmptyState(s)}
		(&Resource{api: api}).Create(ctx, resource.CreateRequest{Plan: plan(s, nil)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(kittest.ErrorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "vm_1" || m.State.ValueString() != client.DynamicIPStateActive || m.CurrentAddress.ValueString() != "102.211.122.90" || m.SurchargePercent.ValueFloat64() != 50 {
			t.Fatalf("state = %+v", m)
		}
		if api.getCalls != 2 {
			t.Fatalf("getCalls = %d, want 2", api.getCalls)
		}
	})

	t.Run("degraded is accepted as settled", func(t *testing.T) {
		api := newFake()
		api.putState = client.DynamicIPStateEnabling
		api.getSeq = []client.DynamicIP{
			{InstanceID: "vm_1", Schedule: "daily", Connections: "keep", State: client.DynamicIPStateDegraded, FailureReason: ptr("reduced capacity"), SurchargePercent: 50, OnDemandLimit: 10},
		}
		resp := resource.CreateResponse{State: kittest.EmptyState(s)}
		(&Resource{api: api}).Create(ctx, resource.CreateRequest{Plan: plan(s, nil)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(kittest.ErrorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.State.ValueString() != client.DynamicIPStateDegraded || m.FailureReason.ValueString() != "reduced capacity" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("failed during enable keeps state and reports failure_reason", func(t *testing.T) {
		api := newFake()
		api.putState = client.DynamicIPStateEnabling
		api.getSeq = []client.DynamicIP{
			{InstanceID: "vm_1", Schedule: "daily", Connections: "keep", State: client.DynamicIPStateFailed, FailureReason: ptr("agent did not answer"), SurchargePercent: 50, OnDemandLimit: 10},
		}
		resp := resource.CreateResponse{State: kittest.EmptyState(s)}
		(&Resource{api: api}).Create(ctx, resource.CreateRequest{Plan: plan(s, nil)}, &resp)
		text := kittest.ErrorText(resp.Diagnostics)
		if !strings.Contains(text, "agent did not answer") {
			t.Fatalf("diags = %s", text)
		}
		if kittest.IsRemoved(resp.State) || getModel(t, resp.State).State.ValueString() != client.DynamicIPStateFailed {
			t.Fatalf("state must be saved with failed status")
		}
	})

	t.Run("timeout keeps pending state", func(t *testing.T) {
		api := newFake()
		api.putState = client.DynamicIPStateEnabling
		api.waitErr = context.DeadlineExceeded
		resp := resource.CreateResponse{State: kittest.EmptyState(s)}
		(&Resource{api: api}).Create(ctx, resource.CreateRequest{Plan: plan(s, nil)}, &resp)
		if !strings.Contains(kittest.ErrorText(resp.Diagnostics), "terraform refresh") {
			t.Fatalf("diags = %s", kittest.ErrorText(resp.Diagnostics))
		}
		if kittest.IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "vm_1" {
			t.Fatal("pending state must be saved before polling")
		}
	})

	t.Run("ADDRESS_ROTATION problem codes get helpful hints", func(t *testing.T) {
		codes := map[string]string{
			client.CodeAddressRotationNotEnabled:          "not enabled for your organization",
			client.CodeAddressRotationOnDemandOff:         "on_demand = false",
			client.CodeAddressRotationNeedsPrivateNetwork: "standard instance",
			client.CodeAddressRotationNeedsAgent:          "platform agent",
			client.CodeAddressRotationNeedsSecurityGroup:  "Narrow the ingress rules",
			client.CodeAddressRotationNoAddress:           "No rotating public IP",
			client.CodeAddressRotationUnavailable:         "not available",
			client.CodeAddressRotationBusy:                "already in progress",
		}
		for code, wantSubstr := range codes {
			t.Run(code, func(t *testing.T) {
				api := newFake()
				api.putErr = &client.APIError{Status: 409, Code: code, Detail: "refused", RequestID: "req_1"}
				resp := resource.CreateResponse{State: kittest.EmptyState(s)}
				(&Resource{api: api}).Create(ctx, resource.CreateRequest{Plan: plan(s, nil)}, &resp)
				text := kittest.ErrorText(resp.Diagnostics)
				if !strings.Contains(text, wantSubstr) || !strings.Contains(text, "req_1") {
					t.Fatalf("diags = %s, want %q", text, wantSubstr)
				}
			})
		}
	})

	t.Run("422 field error points at attribute", func(t *testing.T) {
		api := newFake()
		api.putErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{
			{Field: "schedule", Code: "INVALID_VALUE", Message: "bad schedule"},
		}}
		resp := resource.CreateResponse{State: kittest.EmptyState(s)}
		(&Resource{api: api}).Create(ctx, resource.CreateRequest{Plan: plan(s, nil)}, &resp)
		errs := resp.Diagnostics.Errors()
		withPath, ok := errs[0].(diag.DiagnosticWithPath)
		if len(errs) != 1 || !ok || !withPath.Path().Equal(path.Root("schedule")) {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
	})
}

func TestRead(t *testing.T) {
	s := testSchema(t)

	t.Run("refreshes state", func(t *testing.T) {
		api := newFake()
		api.items["vm_1"] = client.DynamicIP{
			InstanceID: "vm_1", Schedule: "weekly", Connections: "move", OnDemand: true,
			State: client.DynamicIPStateActive, CurrentAddress: ptr("102.211.122.91"),
			SurchargePercent: 40, OnDemandUsed: 3, OnDemandLimit: 10,
		}
		resp := resource.ReadResponse{State: stored(s)}
		(&Resource{api: api}).Read(ctx, resource.ReadRequest{State: stored(s)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(kittest.ErrorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.Schedule.ValueString() != "weekly" || m.Connections.ValueString() != "move" || !m.OnDemand.ValueBool() || m.CurrentAddress.ValueString() != "102.211.122.91" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("not found removes from state", func(t *testing.T) {
		api := newFake()
		resp := resource.ReadResponse{State: stored(s)}
		(&Resource{api: api}).Read(ctx, resource.ReadRequest{State: stored(s)}, &resp)
		if resp.Diagnostics.HasError() || !kittest.IsRemoved(resp.State) {
			t.Fatalf("diags = %s, removed = %v", kittest.ErrorText(resp.Diagnostics), kittest.IsRemoved(resp.State))
		}
	})

	t.Run("state off removes from state", func(t *testing.T) {
		api := newFake()
		api.items["vm_1"] = client.DynamicIP{InstanceID: "vm_1", State: client.DynamicIPStateOff}
		resp := resource.ReadResponse{State: stored(s)}
		(&Resource{api: api}).Read(ctx, resource.ReadRequest{State: stored(s)}, &resp)
		if resp.Diagnostics.HasError() || !kittest.IsRemoved(resp.State) {
			t.Fatalf("diags = %s, removed = %v", kittest.ErrorText(resp.Diagnostics), kittest.IsRemoved(resp.State))
		}
	})

	t.Run("server error keeps state", func(t *testing.T) {
		api := newFake()
		api.getErr = &client.APIError{Status: 500, Code: "INTERNAL", RequestID: "req_9"}
		resp := resource.ReadResponse{State: stored(s)}
		(&Resource{api: api}).Read(ctx, resource.ReadRequest{State: stored(s)}, &resp)
		if !strings.Contains(kittest.ErrorText(resp.Diagnostics), "req_9") || kittest.IsRemoved(resp.State) {
			t.Fatalf("diags = %s", kittest.ErrorText(resp.Diagnostics))
		}
	})
}

func TestUpdate(t *testing.T) {
	s := testSchema(t)
	api := newFake()
	api.items["vm_1"] = client.DynamicIP{InstanceID: "vm_1", Schedule: "daily", Connections: "keep", OnDemand: false, State: client.DynamicIPStateActive}

	resp := resource.UpdateResponse{State: stored(s)}
	(&Resource{api: api}).Update(ctx, resource.UpdateRequest{
		Plan:  plan(s, map[string]tftypes.Value{"id": kittest.Str("vm_1"), "schedule": kittest.Str("weekly"), "connections": kittest.Str("move"), "on_demand": tftypes.NewValue(tftypes.Bool, true)}),
		State: stored(s),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(kittest.ErrorText(resp.Diagnostics))
	}
	m := getModel(t, resp.State)
	if len(api.puts) != 1 || m.Schedule.ValueString() != "weekly" || m.Connections.ValueString() != "move" || !m.OnDemand.ValueBool() {
		t.Fatalf("puts = %d, state = %+v", len(api.puts), m)
	}
}

func TestDelete(t *testing.T) {
	s := testSchema(t)

	t.Run("disables and polls until off", func(t *testing.T) {
		api := newFake()
		api.items["vm_1"] = client.DynamicIP{InstanceID: "vm_1", State: client.DynamicIPStateActive}
		api.delState = client.DynamicIPStateDisabling
		api.getSeq = []client.DynamicIP{
			{InstanceID: "vm_1", State: client.DynamicIPStateDisabling},
			{InstanceID: "vm_1", State: client.DynamicIPStateOff},
		}
		var resp resource.DeleteResponse
		(&Resource{api: api}).Delete(ctx, resource.DeleteRequest{State: stored(s)}, &resp)
		if resp.Diagnostics.HasError() || api.deletes != 1 || api.getCalls != 2 {
			t.Fatalf("diags = %s, deletes = %d, getCalls = %d", kittest.ErrorText(resp.Diagnostics), api.deletes, api.getCalls)
		}
	})

	t.Run("polls until not found", func(t *testing.T) {
		api := newFake()
		api.items["vm_1"] = client.DynamicIP{InstanceID: "vm_1", State: client.DynamicIPStateActive}
		api.delState = client.DynamicIPStateDisabling
		api.getSeq = []client.DynamicIP{
			{InstanceID: "vm_1", State: client.DynamicIPStateDisabling},
		}
		// After delete returns disabling, remove from items so the second poll gets 404.
		var resp resource.DeleteResponse
		(&Resource{api: &deleteRemovesAfterFirst{fakeAPI: api}}).Delete(ctx, resource.DeleteRequest{State: stored(s)}, &resp)
		if resp.Diagnostics.HasError() || api.deletes != 1 || api.getCalls != 2 {
			t.Fatalf("diags = %s, deletes = %d, getCalls = %d", kittest.ErrorText(resp.Diagnostics), api.deletes, api.getCalls)
		}
	})

	t.Run("already gone is success", func(t *testing.T) {
		api := newFake()
		var resp resource.DeleteResponse
		(&Resource{api: api}).Delete(ctx, resource.DeleteRequest{State: stored(s)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("diags = %s", kittest.ErrorText(resp.Diagnostics))
		}
	})
}

type deleteRemovesAfterFirst struct{ *fakeAPI }

func (d *deleteRemovesAfterFirst) DeleteDynamicIP(ctx context.Context, instanceID string) (*client.DynamicIP, error) {
	res, err := d.fakeAPI.DeleteDynamicIP(ctx, instanceID)
	delete(d.items, instanceID)
	return res, err
}

func TestImportState(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid instance id", "vm_123", false},
		{"wrong prefix", "vol_123", true},
		{"empty", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := resource.ImportStateResponse{State: kittest.State(s, nil)}
			(&Resource{api: newFake()}).ImportState(ctx, resource.ImportStateRequest{ID: tt.id}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s", kittest.ErrorText(resp.Diagnostics))
			}
			if !tt.wantErr {
				m := getModel(t, resp.State)
				if m.ID.ValueString() != tt.id || m.InstanceID.ValueString() != tt.id {
					t.Fatalf("state = %+v", m)
				}
			}
		})
	}
}
