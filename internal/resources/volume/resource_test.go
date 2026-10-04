package volume

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// --- helpers: build the plan and state values Terraform would hand the resource ---

func testSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	New().Schema(ctx, resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func newTestResource(api volumeAPI) *Resource { return &Resource{api: api} }

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

func num(v int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }

func unknownStr() tftypes.Value { return tftypes.NewValue(tftypes.String, tftypes.UnknownValue) }

func unknownNum() tftypes.Value { return tftypes.NewValue(tftypes.Number, tftypes.UnknownValue) }

func nullStr() tftypes.Value { return tftypes.NewValue(tftypes.String, nil) }

func emptyState(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(objectType(s), nil)}
}

// stored is the state of the volume vol_1 (small-5gb, detached) with overrides.
func stored(over map[string]tftypes.Value) map[string]tftypes.Value {
	set := map[string]tftypes.Value{
		"id": str("vol_1"), "name": str("data"), "disk_offering_slug": str("small-5gb"), "size_gb": num(5),
		"region": str("af-abj"), "storage_type": str("shared"), "zone": str("af-abj-1"), "observed_state": str("active"),
		"monthly_cost_minor": num(116800), "currency": str("NGN"),
		"created_at": str("2026-10-04T12:10:12Z"), "updated_at": str("2026-10-04T12:11:04Z"),
	}
	for k, v := range over {
		set[k] = v
	}
	return set
}

func stateFor(s schema.Schema, over map[string]tftypes.Value) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: values(s, stored(over))}
}

// updatePlan is the plan for vol_1 with the given changes. Computed values that a
// change would alter are unknown, as Terraform plans them.
func updatePlan(s schema.Schema, over map[string]tftypes.Value) tfsdk.Plan {
	set := stored(map[string]tftypes.Value{"monthly_cost_minor": unknownNum(), "currency": unknownStr(), "observed_state": unknownStr(), "updated_at": unknownStr()})
	for k, v := range over {
		set[k] = v
	}
	return tfsdk.Plan{Schema: s, Raw: values(s, set)}
}

// createPlan is what Terraform passes to Create: user values set, computed ones unknown.
func createPlan(s schema.Schema, over map[string]tftypes.Value) tfsdk.Plan {
	set := map[string]tftypes.Value{
		"id": unknownStr(), "name": str("data"), "disk_offering_slug": str("small-5gb"), "size_gb": unknownNum(),
		"source_snapshot_id": unknownStr(), "region": unknownStr(), "storage_type": unknownStr(), "zone": unknownStr(), "observed_state": unknownStr(),
		"monthly_cost_minor": unknownNum(), "currency": unknownStr(), "created_at": unknownStr(), "updated_at": unknownStr(),
	}
	for k, v := range over {
		set[k] = v
	}
	return tfsdk.Plan{Schema: s, Raw: values(s, set)}
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

func attachedTo(d diag.Diagnostics, p path.Path) bool {
	for _, e := range d.Errors() {
		if withPath, ok := e.(diag.DiagnosticWithPath); ok && withPath.Path().Equal(p) {
			return true
		}
	}
	return false
}

// attachedVolume is vol_1 on 20 GB of local storage, attached to vm_1.
func attachedVolume() client.Volume {
	v := volume("vol_1", "data", "small-local-20gb", 20, "local")
	v.AttachedInstanceID, v.DesiredInstanceID = ptr("vm_1"), ptr("vm_1")
	return v
}

// localState is the stored state of vol_1 as a 20 GB local volume, attached to
// the given instance (null for none).
func localState(instance tftypes.Value) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"instance_id": instance, "disk_offering_slug": str("small-local-20gb"), "size_gb": num(20), "storage_type": str("local"),
	}
}

// --- schema and wiring ---

func TestSchemaIsValid(t *testing.T) {
	if d := testSchema(t).ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func TestMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	New().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &resp)
	if resp.TypeName != "pantechdynamics_volume" {
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
		t.Fatalf("a real client must satisfy volumeAPI: %v", ok.Diagnostics)
	}
}

func TestImportState(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		name, id string
		wantErr  bool
	}{{"valid", "vol_06ggdmjy3ds0hfcxgp8knnyqk4", false}, {"wrong prefix", "vm_1", true}, {"empty", "", true}} {
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

// --- create ---

func TestCreate(t *testing.T) {
	s := testSchema(t)
	create := func(api *fakeAPI, over map[string]tftypes.Value) resource.CreateResponse {
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: createPlan(s, over)}, &resp)
		return resp
	}

	t.Run("a standalone volume", func(t *testing.T) {
		api := seeded()
		resp := create(api, nil)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "vol_1" || m.SizeGB.ValueInt64() != 5 || m.StorageType.ValueString() != "shared" || !m.InstanceID.IsNull() {
			t.Fatalf("state = %+v", m)
		}
		if api.creates != 1 || api.attaches != 0 || api.lastCreate.InstanceID != "" {
			t.Fatalf("creates = %d, attaches = %d: the create-time instance_id must never be used", api.creates, api.attaches)
		}
	})

	t.Run("a local volume attaches in its own step", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"disk_offering_slug": str("small-local-20gb"), "instance_id": str("vm_1")})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if api.creates != 1 || api.attaches != 1 || api.detaches != 0 || m.InstanceID.ValueString() != "vm_1" || m.SizeGB.ValueInt64() != 20 {
			t.Fatalf("creates = %d, attaches = %d, detaches = %d, state = %+v", api.creates, api.attaches, api.detaches, m)
		}
	})

	t.Run("a shared volume that cannot attach says why and clears its stale request", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"instance_id": str("vm_1")})

		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "CLOUDSTACK_JOB_FAILED") || !strings.Contains(text, `"shared"`) || !strings.Contains(text, "can still be deleted") {
			t.Fatalf("diags = %s", text)
		}
		v := api.find("vol_1")
		if api.attaches != 1 || api.detaches != 1 || v.DesiredInstanceID != nil {
			t.Fatalf("attaches = %d, detaches = %d, desired = %v: the stale request must be cleared so the volume stays deletable", api.attaches, api.detaches, v.DesiredInstanceID)
		}
		if resp.State.Raw.IsNull() || getModel(t, resp.State).ID.ValueString() != "vol_1" || !getModel(t, resp.State).InstanceID.IsNull() {
			t.Fatal("the volume exists and is not attached: state must say so")
		}
	})

	t.Run("a fixed offering with a different size is refused before ordering", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"size_gb": num(50)})
		if api.creates != 0 || !attachedTo(resp.Diagnostics, path.Root("size_gb")) || !strings.Contains(errorText(resp.Diagnostics), "would ignore") {
			t.Fatalf("creates = %d, diags = %s", api.creates, errorText(resp.Diagnostics))
		}
	})

	t.Run("a customized offering needs a size", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"disk_offering_slug": str("custom")})
		if api.creates != 0 || !attachedTo(resp.Diagnostics, path.Root("size_gb")) {
			t.Fatalf("creates = %d, diags = %s", api.creates, errorText(resp.Diagnostics))
		}
	})

	t.Run("a customized offering with a size is ordered with it", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"disk_offering_slug": str("custom"), "size_gb": num(50)})
		if resp.Diagnostics.HasError() || api.lastCreate.SizeGB != 50 || getModel(t, resp.State).SizeGB.ValueInt64() != 50 {
			t.Fatalf("diags = %s, request = %+v", errorText(resp.Diagnostics), api.lastCreate)
		}
	})

	t.Run("an unknown offering is refused", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"disk_offering_slug": str("nope")})
		if api.creates != 0 || !attachedTo(resp.Diagnostics, path.Root("disk_offering_slug")) {
			t.Fatalf("creates = %d, diags = %s", api.creates, errorText(resp.Diagnostics))
		}
	})

	t.Run("a duplicate name is refused", func(t *testing.T) {
		api := seeded(volume("vol_7", "data", "small-5gb", 5, "shared"))
		resp := create(api, nil)
		text := errorText(resp.Diagnostics)
		if api.creates != 0 || !strings.Contains(text, "already exists") || !strings.Contains(text, "terraform import") {
			t.Fatalf("creates = %d, diags = %s", api.creates, text)
		}
	})

	t.Run("an offerings lookup failure stops the order", func(t *testing.T) {
		api := seeded()
		api.offeringsErr = errConnReset
		if resp := create(api, nil); !resp.Diagnostics.HasError() || api.creates != 0 {
			t.Fatalf("creates = %d", api.creates)
		}
	})

	t.Run("a field error from the API points at the attribute", func(t *testing.T) {
		api := seeded()
		api.createErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "region", Code: "REGION_NOT_AVAILABLE", Message: "not available"}}}
		resp := create(api, nil)
		if !attachedTo(resp.Diagnostics, path.Root("region")) || !resp.State.Raw.IsNull() {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})

	t.Run("a lost response is adopted and waited for", func(t *testing.T) {
		api := seeded()
		api.createErr, api.createLands = errConnReset, true
		resp := create(api, nil)
		if resp.Diagnostics.HasError() || getModel(t, resp.State).ID.ValueString() != "vol_1" || api.creates != 1 {
			t.Fatalf("diags = %s, creates = %d", errorText(resp.Diagnostics), api.creates)
		}
	})

	t.Run("a timeout keeps the id in state", func(t *testing.T) {
		api := seeded()
		api.pendingOpErr = context.DeadlineExceeded
		resp := create(api, nil)
		if !strings.Contains(errorText(resp.Diagnostics), "terraform refresh") || getModel(t, resp.State).ID.ValueString() != "vol_1" {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
}

// --- restore from a snapshot ---

func TestCreateFromASnapshot(t *testing.T) {
	s := testSchema(t)
	create := func(api *fakeAPI, over map[string]tftypes.Value) resource.CreateResponse {
		over["source_snapshot_id"] = str("snap_9")
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: createPlan(s, over)}, &resp)
		return resp
	}

	t.Run("restores instead of creating", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if api.restores != 1 || api.creates != 0 || api.lastRestore.snapshotID != "snap_9" || api.lastRestore.offering != "small-5gb" || api.lastRestore.sizeGB != 0 {
			t.Fatalf("restores = %d, creates = %d, call = %+v", api.restores, api.creates, api.lastRestore)
		}
		if m.SourceSnapshotID.ValueString() != "snap_9" || m.ObservedState.ValueString() != "active" || m.SizeGB.ValueInt64() != 5 {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("a customized offering passes its size", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"disk_offering_slug": str("custom"), "size_gb": num(50)})
		if resp.Diagnostics.HasError() || api.lastRestore.sizeGB != 50 {
			t.Fatalf("diags = %s, call = %+v", errorText(resp.Diagnostics), api.lastRestore)
		}
	})

	t.Run("a duplicate name is still refused before restoring", func(t *testing.T) {
		api := seeded(volume("vol_7", "data", "small-5gb", 5, "shared"))
		resp := create(api, map[string]tftypes.Value{})
		if api.restores != 0 || !strings.Contains(errorText(resp.Diagnostics), "already exists") {
			t.Fatalf("restores = %d, diags = %s", api.restores, errorText(resp.Diagnostics))
		}
	})

	t.Run("a failed restore keeps the failed volume in state, warns, and the volume can be deleted", func(t *testing.T) {
		api := seeded()
		api.restoreFails = true
		resp := create(api, map[string]tftypes.Value{})

		if !strings.Contains(errorText(resp.Diagnostics), "createVolume failed") {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
		warned := false
		for _, d := range resp.Diagnostics.Warnings() {
			warned = warned || strings.Contains(d.Detail(), "staging platform every restore")
		}
		m := getModel(t, resp.State)
		if !warned || m.ID.ValueString() != "vol_1" || m.ObservedState.ValueString() != "failed" || m.SourceSnapshotID.ValueString() != "snap_9" {
			t.Fatalf("warned = %v, state = %+v: the failed volume exists, so state must track it", warned, m)
		}

		// the next apply deletes it
		var del resource.DeleteResponse
		newTestResource(api).Delete(ctx, resource.DeleteRequest{State: resp.State}, &del)
		if del.Diagnostics.HasError() || api.find("vol_1").ObservedState != client.VolumeDeleted {
			t.Fatalf("diags = %s", errorText(del.Diagnostics))
		}
	})
}

func TestSnapshotIDCheck(t *testing.T) {
	for value, wantErr := range map[string]bool{"snap_1": false, "vol_1": true, "": true} {
		var resp validator.StringResponse
		snapshotIDCheck.ValidateString(ctx, validator.StringRequest{ConfigValue: types.StringValue(value)}, &resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%q: diags = %v", value, resp.Diagnostics)
		}
	}
}

// --- read ---

func TestRead(t *testing.T) {
	s := testSchema(t)
	read := func(api *fakeAPI) resource.ReadResponse {
		resp := resource.ReadResponse{State: stateFor(s, nil)}
		newTestResource(api).Read(ctx, resource.ReadRequest{State: stateFor(s, nil)}, &resp)
		return resp
	}

	t.Run("refreshes and reports the real attachment", func(t *testing.T) {
		v := attachedVolume()
		resp := read(seeded(v))
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if m := getModel(t, resp.State); m.InstanceID.ValueString() != "vm_1" || m.SizeGB.ValueInt64() != 20 {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("gone is removed from state", func(t *testing.T) {
		if resp := read(seeded()); resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
	})

	t.Run("a deleted volume that still reads 200 is removed from state", func(t *testing.T) {
		v := volume("vol_1", "data", "small-5gb", 5, "shared")
		v.ObservedState = client.VolumeDeleted
		if resp := read(seeded(v)); !resp.State.Raw.IsNull() {
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

// --- update ---

func TestUpdate(t *testing.T) {
	s := testSchema(t)
	update := func(api *fakeAPI, stateOver, planOver map[string]tftypes.Value) resource.UpdateResponse {
		resp := resource.UpdateResponse{State: stateFor(s, stateOver)}
		newTestResource(api).Update(ctx, resource.UpdateRequest{Plan: updatePlan(s, planOver), State: stateFor(s, stateOver)}, &resp)
		return resp
	}
	detachedLocal := func() *fakeAPI { return seeded(volume("vol_1", "data", "small-local-20gb", 20, "local")) }

	t.Run("attaches a detached volume", func(t *testing.T) {
		api := detachedLocal()
		resp := update(api, localState(nullStr()), localState(str("vm_1")))
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.attaches != 1 || api.detaches != 0 || getModel(t, resp.State).InstanceID.ValueString() != "vm_1" {
			t.Fatalf("attaches = %d, detaches = %d", api.attaches, api.detaches)
		}
	})

	t.Run("detaches when instance_id is removed", func(t *testing.T) {
		api := seeded(attachedVolume())
		resp := update(api, localState(str("vm_1")), localState(nullStr()))
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.detaches != 1 || api.redundantDetaches != 0 || !getModel(t, resp.State).InstanceID.IsNull() {
			t.Fatalf("detaches = %d, redundant = %d", api.detaches, api.redundantDetaches)
		}
	})

	t.Run("moves to another instance by detaching first", func(t *testing.T) {
		api := seeded(attachedVolume())
		resp := update(api, localState(str("vm_1")), localState(str("vm_2")))
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.detaches != 1 || api.attaches != 1 || getModel(t, resp.State).InstanceID.ValueString() != "vm_2" {
			t.Fatalf("detaches = %d, attaches = %d", api.detaches, api.attaches)
		}
	})

	t.Run("grows a detached volume in place", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "small-5gb", 5, "shared"))
		resp := update(api, nil, map[string]tftypes.Value{"disk_offering_slug": str("shared-10gb"), "size_gb": unknownNum()})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if api.resizes != 1 || api.refused != 0 || m.SizeGB.ValueInt64() != 10 || m.DiskOfferingSlug.ValueString() != "shared-10gb" {
			t.Fatalf("resizes = %d, refused = %d, state = %+v", api.resizes, api.refused, m)
		}
	})

	t.Run("detaching and growing in one change detaches first", func(t *testing.T) {
		api := seeded(attachedVolume())
		resp := update(api, localState(str("vm_1")),
			map[string]tftypes.Value{"instance_id": nullStr(), "disk_offering_slug": str("local-40gb"), "size_gb": unknownNum(), "storage_type": str("local")})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if api.detaches != 1 || api.resizes != 1 || api.refused != 0 || m.SizeGB.ValueInt64() != 40 || !m.InstanceID.IsNull() {
			t.Fatalf("detaches = %d, resizes = %d, refused = %d, state = %+v", api.detaches, api.resizes, api.refused, m)
		}
	})

	t.Run("growing and attaching in one change grows first", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "small-local-20gb", 20, "local"))
		resp := update(api, localState(nullStr()),
			map[string]tftypes.Value{"instance_id": str("vm_1"), "disk_offering_slug": str("local-40gb"), "size_gb": unknownNum(), "storage_type": str("local")})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if api.resizes != 1 || api.attaches != 1 || api.refused != 0 || m.SizeGB.ValueInt64() != 40 || m.InstanceID.ValueString() != "vm_1" {
			t.Fatalf("resizes = %d, attaches = %d, refused = %d, state = %+v", api.resizes, api.attaches, api.refused, m)
		}
	})

	t.Run("a smaller offering is refused before anything is sent", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "shared-10gb", 10, "shared"))
		resp := update(api, map[string]tftypes.Value{"disk_offering_slug": str("shared-10gb"), "size_gb": num(10)},
			map[string]tftypes.Value{"disk_offering_slug": str("small-5gb"), "size_gb": unknownNum()})
		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "only grow") || !strings.Contains(text, "-replace") || api.resizes != 0 || !attachedTo(resp.Diagnostics, path.Root("disk_offering_slug")) {
			t.Fatalf("diags = %s, resizes = %d", text, api.resizes)
		}
	})

	t.Run("an attach that fails keeps the earlier resize in state and clears the stale request", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "small-5gb", 5, "shared"))
		resp := update(api, nil, map[string]tftypes.Value{
			"instance_id": str("vm_1"), "disk_offering_slug": str("shared-10gb"), "size_gb": unknownNum(),
		})
		if !strings.Contains(errorText(resp.Diagnostics), "CLOUDSTACK_JOB_FAILED") {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.SizeGB.ValueInt64() != 10 || !m.InstanceID.IsNull() || api.find("vol_1").DesiredInstanceID != nil {
			t.Fatalf("state = %+v: the resize succeeded and must not be lost", m)
		}
	})

	t.Run("a stuck operation finishes when the volume reaches the goal", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "small-5gb", 5, "shared"))
		api.opStuck = true
		resp := update(api, nil, map[string]tftypes.Value{"disk_offering_slug": str("shared-10gb"), "size_gb": unknownNum()})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})

	t.Run("nothing to change sends nothing", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "small-5gb", 5, "shared"))
		resp := update(api, nil, nil)
		if resp.Diagnostics.HasError() || api.attaches+api.detaches+api.resizes != 0 {
			t.Fatalf("attaches = %d, detaches = %d, resizes = %d", api.attaches, api.detaches, api.resizes)
		}
	})
}

// --- plan time ---

func TestModifyPlanRefusesAResizeWhileAttached(t *testing.T) {
	s := testSchema(t)
	run := func(stateOver, planOver map[string]tftypes.Value) resource.ModifyPlanResponse {
		var resp resource.ModifyPlanResponse
		newTestResource(seeded()).ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: updatePlan(s, planOver), State: stateFor(s, stateOver)}, &resp)
		return resp
	}
	attached := localState(str("vm_1"))

	t.Run("a bigger offering while still attached is refused", func(t *testing.T) {
		resp := run(attached, map[string]tftypes.Value{"instance_id": str("vm_1"), "disk_offering_slug": str("local-40gb"), "size_gb": unknownNum(), "storage_type": str("local")})
		if !attachedTo(resp.Diagnostics, path.Root("disk_offering_slug")) || !strings.Contains(errorText(resp.Diagnostics), "Detach the volume") {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
	t.Run("a bigger offering with instance_id cleared is fine", func(t *testing.T) {
		resp := run(attached, map[string]tftypes.Value{"instance_id": nullStr(), "disk_offering_slug": str("local-40gb"), "size_gb": unknownNum(), "storage_type": str("local")})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
	t.Run("a bigger offering while detached is fine", func(t *testing.T) {
		resp := run(nil, map[string]tftypes.Value{"disk_offering_slug": str("shared-10gb"), "size_gb": unknownNum()})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
	t.Run("staying attached with no resize is fine", func(t *testing.T) {
		resp := run(attached, attached)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
	t.Run("a create is skipped", func(t *testing.T) {
		var resp resource.ModifyPlanResponse
		newTestResource(seeded()).ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: createPlan(s, nil), State: emptyState(s)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
}

func TestSizeFollowsOffering(t *testing.T) {
	s := testSchema(t)
	run := func(planOver, stateOver map[string]tftypes.Value, config types.Int64, proposed types.Int64) types.Int64 {
		req := planmodifier.Int64Request{
			Path: path.Root("size_gb"), Plan: updatePlan(s, planOver), State: stateFor(s, stateOver),
			ConfigValue: config, PlanValue: proposed, StateValue: types.Int64Value(5),
		}
		resp := planmodifier.Int64Response{PlanValue: proposed}
		sizeFollowsOffering{}.PlanModifyInt64(ctx, req, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		return resp.PlanValue
	}

	t.Run("an unchanged offering keeps the stored size", func(t *testing.T) {
		if got := run(nil, nil, types.Int64Null(), types.Int64Unknown()); got.IsUnknown() || got.ValueInt64() != 5 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("a changed offering with no configured size makes it unknown", func(t *testing.T) {
		if got := run(map[string]tftypes.Value{"disk_offering_slug": str("shared-10gb")}, nil, types.Int64Null(), types.Int64Value(5)); !got.IsUnknown() {
			t.Fatalf("got %v: the size changes with the offering, so it must not be planned as the old size", got)
		}
	})
	t.Run("a configured size is left alone", func(t *testing.T) {
		if got := run(map[string]tftypes.Value{"disk_offering_slug": str("custom")}, nil, types.Int64Value(50), types.Int64Value(50)); got.ValueInt64() != 50 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("a create is left alone", func(t *testing.T) {
		req := planmodifier.Int64Request{Path: path.Root("size_gb"), Plan: createPlan(s, nil), State: emptyState(s), ConfigValue: types.Int64Null(), PlanValue: types.Int64Unknown()}
		resp := planmodifier.Int64Response{PlanValue: types.Int64Unknown()}
		sizeFollowsOffering{}.PlanModifyInt64(ctx, req, &resp)
		if !resp.PlanValue.IsUnknown() {
			t.Fatalf("got %v", resp.PlanValue)
		}
	})
}

// --- delete ---

func TestDelete(t *testing.T) {
	s := testSchema(t)
	del := func(api *fakeAPI) resource.DeleteResponse {
		var resp resource.DeleteResponse
		newTestResource(api).Delete(ctx, resource.DeleteRequest{State: stateFor(s, nil)}, &resp)
		return resp
	}

	t.Run("deletes a detached volume", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "small-5gb", 5, "shared"))
		if resp := del(api); resp.Diagnostics.HasError() || api.deletes != 1 || api.detaches != 0 || api.refused != 0 {
			t.Fatalf("deletes = %d, detaches = %d, refused = %d, diags = %v", api.deletes, api.detaches, api.refused, resp.Diagnostics)
		}
	})

	t.Run("detaches an attached volume first", func(t *testing.T) {
		api := seeded(attachedVolume())
		resp := del(api)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.detaches != 1 || api.redundantDetaches != 0 || api.deletes != 1 || api.refused != 0 {
			t.Fatalf("detaches = %d, redundant = %d, deletes = %d, refused = %d", api.detaches, api.redundantDetaches, api.deletes, api.refused)
		}
	})

	t.Run("clears the stale request a failed attach left, so the delete is accepted", func(t *testing.T) {
		stale := volume("vol_1", "data", "small-5gb", 5, "shared")
		stale.DesiredInstanceID = ptr("vm_1") // not attached, but asked to be
		api := seeded(stale)
		resp := del(api)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.redundantDetaches != 1 || api.deletes != 1 || api.refused != 0 {
			t.Fatalf("redundant detaches = %d, deletes = %d, refused = %d: the delete would have been refused without the clear", api.redundantDetaches, api.deletes, api.refused)
		}
	})

	t.Run("already deleted counts as success", func(t *testing.T) {
		v := volume("vol_1", "data", "small-5gb", 5, "shared")
		v.ObservedState = client.VolumeDeleted
		api := seeded(v)
		if resp := del(api); resp.Diagnostics.HasError() || api.deletes != 0 {
			t.Fatalf("deletes = %d, diags = %v", api.deletes, resp.Diagnostics)
		}
	})

	t.Run("a volume that never existed counts as deleted", func(t *testing.T) {
		api := seeded()
		if resp := del(api); resp.Diagnostics.HasError() || api.deletes != 0 {
			t.Fatalf("deletes = %d, diags = %v", api.deletes, resp.Diagnostics)
		}
	})

	t.Run("a refusal explains what to do", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "small-5gb", 5, "shared"))
		api.deleteErr = &client.APIError{Status: 409, Code: client.CodeInvalidResourceState}
		if text := errorText(del(api).Diagnostics); !strings.Contains(text, "Detach it first") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a stuck operation finishes when the volume reads as deleted", func(t *testing.T) {
		api := seeded(volume("vol_1", "data", "small-5gb", 5, "shared"))
		api.opStuck = true
		if resp := del(api); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
}
