package snapshot

import (
	"context"
	"strings"
	"testing"

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

// --- helpers: build the plan, config and state values Terraform would hand the resource ---

func testSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	New().Schema(ctx, resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func newTestResource(api snapshotAPI) *Resource { return &Resource{api: api} }

func objectType(s schema.Schema) tftypes.Object {
	obj, ok := s.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		panic("the schema type is not an object")
	}
	return obj
}

// values returns an object value with every attribute null except those set.
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

func unknownStr() tftypes.Value { return tftypes.NewValue(tftypes.String, tftypes.UnknownValue) }

func unknownNum() tftypes.Value { return tftypes.NewValue(tftypes.Number, tftypes.UnknownValue) }

func emptyState(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(objectType(s), nil)}
}

// createPlan is what Terraform passes to Create: user values set, computed ones unknown.
func createPlan(s schema.Schema, source map[string]tftypes.Value) tfsdk.Plan {
	set := map[string]tftypes.Value{
		"id": unknownStr(), "name": str("nightly"), "observed_state": unknownStr(), "trigger": unknownStr(),
		"size_bytes": unknownNum(), "completed_at": unknownStr(), "created_at": unknownStr(),
	}
	for k, v := range source {
		set[k] = v
	}
	return tfsdk.Plan{Schema: s, Raw: values(s, set)}
}

var (
	instanceSource = map[string]tftypes.Value{"instance_id": str("vm_1")}
	volumeSource   = map[string]tftypes.Value{"volume_id": str("vol_1")}
)

// stored is the state of snapshot snap_1 of instance vm_1.
func stored() map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"id": str("snap_1"), "name": str("nightly"), "instance_id": str("vm_1"), "observed_state": str("active"), "trigger": str("manual"),
		"size_bytes": tftypes.NewValue(tftypes.Number, 196928), "completed_at": str("2026-10-04T13:36:50Z"), "created_at": str("2026-10-04T13:36:50Z"),
	}
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

func seeded(snaps ...client.Snapshot) *fakeAPI { return &fakeAPI{snaps: snaps, nextID: len(snaps)} }

// --- schema and wiring ---

func TestSchemaIsValid(t *testing.T) {
	if d := testSchema(t).ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func TestMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	New().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &resp)
	if resp.TypeName != "pantechdynamics_snapshot" {
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
		t.Fatalf("a real client must satisfy snapshotAPI: %v", ok.Diagnostics)
	}
}

func TestValidateConfigNeedsExactlyOneSource(t *testing.T) {
	s := testSchema(t)
	validate := func(set map[string]tftypes.Value) resource.ValidateConfigResponse {
		base := map[string]tftypes.Value{"name": str("nightly")}
		for k, v := range set {
			base[k] = v
		}
		var resp resource.ValidateConfigResponse
		(&Resource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: values(s, base)}}, &resp)
		return resp
	}

	tests := []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
	}{
		{"an instance", instanceSource, false},
		{"a volume", volumeSource, false},
		{"neither", nil, true},
		{"both", map[string]tftypes.Value{"instance_id": str("vm_1"), "volume_id": str("vol_1")}, true},
		{"an unknown instance (not created yet) counts as set", map[string]tftypes.Value{"instance_id": unknownStr()}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if resp := validate(tt.set); resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s", errorText(resp.Diagnostics))
			}
		})
	}
}

func TestImportState(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		name, id string
		wantErr  bool
	}{{"valid", "snap_06gge7z00ntc958r3qa4dm3364", false}, {"wrong prefix", "vol_1", true}, {"empty", "", true}} {
		t.Run(tt.name, func(t *testing.T) {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: values(s, nil)}}
			newTestResource(seeded()).ImportState(ctx, resource.ImportStateRequest{ID: tt.id}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s", errorText(resp.Diagnostics))
			}
			if !tt.wantErr && getModel(t, resp.State).ID.ValueString() != tt.id {
				t.Fatal("id not set")
			}
		})
	}
}

func TestValidators(t *testing.T) {
	tests := []struct {
		name    string
		check   stringCheck
		value   types.String
		wantErr bool
	}{
		{"name ok", nameCheck, types.StringValue("nightly"), false},
		{"name with spaces is ok", nameCheck, types.StringValue("before upgrade"), false},
		{"name empty", nameCheck, types.StringValue(""), true},
		{"name blank", nameCheck, types.StringValue("  "), true},
		{"name 255", nameCheck, types.StringValue(strings.Repeat("a", 255)), false},
		{"name 256", nameCheck, types.StringValue(strings.Repeat("a", 256)), true},
		{"name null", nameCheck, types.StringNull(), false},
		{"instance id ok", prefixCheck("vm_", "an instance"), types.StringValue("vm_1"), false},
		{"instance id wrong kind", prefixCheck("vm_", "an instance"), types.StringValue("vol_1"), true},
		{"volume id ok", prefixCheck("vol_", "a volume"), types.StringValue("vol_1"), false},
		{"volume id wrong kind", prefixCheck("vol_", "a volume"), types.StringValue("vm_1"), true},
		{"unknown is skipped", prefixCheck("vm_", "an instance"), types.StringUnknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.StringResponse
			tt.check.ValidateString(ctx, validator.StringRequest{ConfigValue: tt.value}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %v", resp.Diagnostics)
			}
		})
	}
}

// --- create ---

func TestCreate(t *testing.T) {
	s := testSchema(t)
	create := func(api *fakeAPI, source map[string]tftypes.Value) resource.CreateResponse {
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: createPlan(s, source)}, &resp)
		return resp
	}

	t.Run("an instance snapshot", func(t *testing.T) {
		api := seeded()
		resp := create(api, instanceSource)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "snap_1" || m.InstanceID.ValueString() != "vm_1" || !m.VolumeID.IsNull() || m.ObservedState.ValueString() != "active" || m.SizeBytes.ValueInt64() != 196928 || m.Trigger.ValueString() != "manual" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("a volume snapshot", func(t *testing.T) {
		resp := create(seeded(), volumeSource)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if m := getModel(t, resp.State); m.VolumeID.ValueString() != "vol_1" || !m.InstanceID.IsNull() {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("a name already used says failed and deleted snapshots keep their name", func(t *testing.T) {
		api := seeded(snap("snap_7", "nightly", "vm_1", ""))
		resp := create(api, instanceSource)
		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "keep their name") || !strings.Contains(text, "terraform import") || !resp.State.Raw.IsNull() {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("the same name on another source is fine", func(t *testing.T) {
		api := seeded(snap("snap_7", "nightly", "vm_other", ""))
		if resp := create(api, instanceSource); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})

	t.Run("a snapshot that fails explains when snapshots work and keeps the id", func(t *testing.T) {
		api := seeded()
		api.failNext = true
		resp := create(api, instanceSource)

		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "CLOUDSTACK_JOB_FAILED") || !strings.Contains(text, "stopped instance") || !strings.Contains(text, "deletes it and tries again") || !strings.Contains(text, "SNAPSHOT_NAME_TAKEN") {
			t.Fatalf("diags = %s", text)
		}
		if n := strings.Count(text, "On the staging platform"); n != 1 {
			t.Fatalf("the hint appears %d times, want once: %s", n, text)
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "snap_1" || m.ObservedState.ValueString() != "failed" {
			t.Fatalf("state = %+v: the failed snapshot exists and holds its name, so state must track it", m)
		}
	})

	t.Run("a snapshot found failed by the done check reports the state, and the hint is added once by the caller", func(t *testing.T) {
		failed := snap("snap_1", "nightly", "vm_1", "")
		failed.ObservedState = client.SnapshotFailed

		_, err := newTestResource(seeded(failed)).isActive("snap_1")(ctx)

		if err == nil || !strings.Contains(err.Error(), "ended in the failed state") || strings.Contains(err.Error(), "On the staging platform") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a failed volume snapshot gives the volume hint", func(t *testing.T) {
		api := seeded()
		api.failNext = true
		if text := errorText(create(api, volumeSource).Diagnostics); !strings.Contains(text, "attached to an instance succeeded") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a lost response is adopted and never re-ordered", func(t *testing.T) {
		api := seeded()
		api.createErr, api.createLands = errConnReset, true
		resp := create(api, instanceSource)
		if resp.Diagnostics.HasError() || api.creates != 1 || getModel(t, resp.State).ID.ValueString() != "snap_1" {
			t.Fatalf("diags = %s, creates = %d", errorText(resp.Diagnostics), api.creates)
		}
	})

	t.Run("an ambiguous failure with nothing found returns the cause", func(t *testing.T) {
		api := seeded()
		api.createErr = errConnReset
		resp := create(api, instanceSource)
		if !strings.Contains(errorText(resp.Diagnostics), "connection reset") || !resp.State.Raw.IsNull() {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})

	t.Run("a timeout keeps the id", func(t *testing.T) {
		api := seeded()
		api.pendingOpErr = context.DeadlineExceeded
		resp := create(api, instanceSource)
		if !strings.Contains(errorText(resp.Diagnostics), "terraform refresh") || getModel(t, resp.State).ID.ValueString() != "snap_1" {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})

	t.Run("an operation that never completes is fine when the snapshot is active", func(t *testing.T) {
		api := seeded()
		api.opStuck = true
		if resp := create(api, instanceSource); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
}

func TestPlaceSnapshotNeverOrdersTwice(t *testing.T) {
	m := model{Name: types.StringValue("nightly"), InstanceID: types.StringValue("vm_1"), VolumeID: types.StringNull()}
	for _, tt := range []struct {
		name string
		api  *fakeAPI
	}{
		{"transport error, order landed", func() *fakeAPI { f := seeded(); f.createErr, f.createLands = errConnReset, true; return f }()},
		{"502, order landed", func() *fakeAPI {
			f := seeded()
			f.createErr, f.createLands = &client.APIError{Status: 502}, true
			return f
		}()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := placeSnapshot(ctx, tt.api, m)
			if err != nil || got.SnapshotID != "snap_1" || got.OperationID != "" || tt.api.creates != 1 {
				t.Fatalf("got = %+v, err = %v, creates = %d", got, err, tt.api.creates)
			}
		})
	}

	t.Run("a definite answer is never looked up", func(t *testing.T) {
		api := seeded()
		api.createErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED"}
		if _, err := placeSnapshot(ctx, api, m); err == nil || api.creates != 1 {
			t.Fatalf("err = %v, creates = %d", err, api.creates)
		}
	})
}

// --- read, update, delete ---

func TestRead(t *testing.T) {
	s := testSchema(t)
	read := func(api *fakeAPI) resource.ReadResponse {
		resp := resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: values(s, stored())}}
		newTestResource(api).Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: values(s, stored())}}, &resp)
		return resp
	}

	t.Run("refreshes", func(t *testing.T) {
		resp := read(seeded(snap("snap_1", "nightly", "vm_1", "")))
		if resp.Diagnostics.HasError() || getModel(t, resp.State).ObservedState.ValueString() != "active" {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
	t.Run("a failed snapshot stays in state, visibly failed", func(t *testing.T) {
		failed := snap("snap_1", "nightly", "vm_1", "")
		failed.ObservedState = client.SnapshotFailed
		resp := read(seeded(failed))
		if resp.State.Raw.IsNull() || getModel(t, resp.State).ObservedState.ValueString() != "failed" {
			t.Fatal("a failed snapshot still exists, so it stays in state")
		}
	})
	t.Run("gone is removed from state", func(t *testing.T) {
		if resp := read(seeded()); !resp.State.Raw.IsNull() {
			t.Fatal("state should be removed")
		}
	})
	t.Run("a deleted snapshot that still reads 200 is removed from state", func(t *testing.T) {
		deleted := snap("snap_1", "nightly", "vm_1", "")
		deleted.ObservedState = client.SnapshotDeleted
		if resp := read(seeded(deleted)); !resp.State.Raw.IsNull() {
			t.Fatal("observed_state deleted means gone")
		}
	})
	t.Run("a server error is reported and state is kept", func(t *testing.T) {
		api := seeded()
		api.getErr = &client.APIError{Status: 500, Code: "INTERNAL", RequestID: "req_7"}
		resp := read(api)
		if !strings.Contains(errorText(resp.Diagnostics), "req_7") || resp.State.Raw.IsNull() {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
}

func TestUpdateOnlyRefreshes(t *testing.T) {
	s := testSchema(t)
	api := seeded(snap("snap_1", "nightly", "vm_1", ""))
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: values(s, stored())}}

	newTestResource(api).Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan{Schema: s, Raw: values(s, stored())}}, &resp)

	if resp.Diagnostics.HasError() || api.creates != 0 || api.deletes != 0 {
		t.Fatalf("diags = %s, creates = %d, deletes = %d", errorText(resp.Diagnostics), api.creates, api.deletes)
	}
}

func TestDelete(t *testing.T) {
	s := testSchema(t)
	del := func(api *fakeAPI) resource.DeleteResponse {
		var resp resource.DeleteResponse
		newTestResource(api).Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: values(s, stored())}}, &resp)
		return resp
	}

	t.Run("deletes", func(t *testing.T) {
		api := seeded(snap("snap_1", "nightly", "vm_1", ""))
		if resp := del(api); resp.Diagnostics.HasError() || api.deletes != 1 {
			t.Fatalf("deletes = %d, diags = %s", api.deletes, errorText(resp.Diagnostics))
		}
	})
	t.Run("a failed snapshot can be deleted too", func(t *testing.T) {
		failed := snap("snap_1", "nightly", "vm_1", "")
		failed.ObservedState = client.SnapshotFailed
		api := seeded(failed)
		if resp := del(api); resp.Diagnostics.HasError() || api.deletes != 1 {
			t.Fatalf("deletes = %d, diags = %s", api.deletes, errorText(resp.Diagnostics))
		}
	})
	t.Run("already gone counts as success", func(t *testing.T) {
		if resp := del(seeded()); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
	t.Run("an error is reported", func(t *testing.T) {
		api := seeded(snap("snap_1", "nightly", "vm_1", ""))
		api.deleteErr = &client.APIError{Status: 409, Code: client.CodeInvalidResourceState}
		if !del(api).Diagnostics.HasError() {
			t.Fatal("want an error")
		}
	})
	t.Run("a stuck operation finishes when the snapshot reads as deleted", func(t *testing.T) {
		api := seeded(snap("snap_1", "nightly", "vm_1", ""))
		api.opStuck = true
		if resp := del(api); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
}
