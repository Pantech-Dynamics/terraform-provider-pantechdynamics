package instance

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
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

func newTestResource(api instanceAPI) *Resource { return &Resource{api: api} }

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

func unknown() tftypes.Value { return tftypes.NewValue(tftypes.String, tftypes.UnknownValue) }

func unknownMap() tftypes.Value {
	return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, tftypes.UnknownValue)
}

func emptyState(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(objectType(s), nil)}
}

// planFor is what Terraform passes to Create or Update: user values set, computed ones unknown.
func planFor(s schema.Schema, id, name string, extra map[string]tftypes.Value) tfsdk.Plan {
	set := map[string]tftypes.Value{
		"id": unknown(), "name": str(name), "plan_slug": str("individual"), "image_slug": str("ubuntu-24-04"),
		"region": unknown(), "security_group_id": unknown(), "tags": unknownMap(),
		"observed_state": unknown(), "zone": unknown(), "public_ipv4": unknown(), "private_ipv4": unknown(),
		"created_at": unknown(), "updated_at": unknown(),
	}
	if id != "" {
		set["id"] = str(id)
	}
	for k, v := range extra {
		set[k] = v
	}
	return tfsdk.Plan{Schema: s, Raw: values(s, set)}
}

// stateFor is the stored state of the instance vm_1, named "web".
func stateFor(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: values(s, map[string]tftypes.Value{
		"id": str("vm_1"), "name": str("web"), "plan_slug": str("individual"), "image_slug": str("ubuntu-24-04"),
		"ssh_key_id": str("sshk_1"), "region": str("af-abj"), "security_group_id": str("sg_default"),
		"tags":           tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{}),
		"observed_state": str("running"), "zone": str("af-abj-1"), "private_ipv4": str("102.211.122.77"),
		"created_at": str("2026-10-03T23:38:52Z"), "updated_at": str("2026-10-03T23:39:25Z"),
	})}
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

func seeded(instances ...client.Instance) *fakeAPI {
	return &fakeAPI{instances: instances, nextID: len(instances)}
}

// --- tests ---

func TestSchemaIsValid(t *testing.T) {
	if d := testSchema(t).ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func TestMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	New().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &resp)
	if resp.TypeName != "pantechdynamics_instance" {
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
		t.Fatalf("a real client must satisfy instanceAPI: %v", ok.Diagnostics)
	}
}

func TestCreate(t *testing.T) {
	s := testSchema(t)
	create := func(api *fakeAPI, name string, extra map[string]tftypes.Value) resource.CreateResponse {
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "", name, extra)}, &resp)
		return resp
	}
	withKey := map[string]tftypes.Value{"ssh_key_id": str("sshk_1")}

	t.Run("success", func(t *testing.T) {
		api := seeded()
		resp := create(api, "web", withKey)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "vm_1" || m.ObservedState.ValueString() != "running" || m.Zone.ValueString() != "af-abj-1" || m.SSHKeyID.ValueString() != "sshk_1" {
			t.Fatalf("state = %+v", m)
		}
		if api.creates != 1 || api.orderWaits != 1 || api.untilWaits != 1 {
			t.Fatalf("creates = %d, order waits = %d, until waits = %d", api.creates, api.orderWaits, api.untilWaits)
		}
		if api.lastCreate.Region != "" || api.lastCreate.SecurityGroupID != "" || api.lastCreate.SSHKeyID != "sshk_1" {
			t.Fatalf("request = %+v: unset optionals must be omitted", api.lastCreate)
		}
	})

	t.Run("no credit says so and charges nothing", func(t *testing.T) {
		api := seeded()
		api.createErr = &client.APIError{Status: 402, Code: client.CodeInsufficientCredit,
			Detail: "This instance needs NGN 15,040.00 of credit upfront and NGN 1,000.00 is available."}

		resp := create(api, "web", withKey)

		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "Not enough credit") || !strings.Contains(text, "NGN 15,040.00") || !strings.Contains(text, "nothing was charged") {
			t.Fatalf("diags = %s", text)
		}
		if api.creates != 1 || !resp.State.Raw.IsNull() {
			t.Fatalf("creates = %d: nothing should be saved when the order is refused", api.creates)
		}
	})

	t.Run("a duplicate name is refused before any order", func(t *testing.T) {
		api := seeded(running("vm_7", "web"))
		resp := create(api, "web", withKey)

		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "already exists") || !strings.Contains(text, "terraform import") || !strings.Contains(text, "vm_7") {
			t.Fatalf("diags = %s", text)
		}
		if api.creates != 0 || !resp.State.Raw.IsNull() {
			t.Fatalf("creates = %d", api.creates)
		}
	})

	t.Run("validation errors point at the attribute", func(t *testing.T) {
		api := seeded()
		api.createErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED",
			Errors: []client.FieldError{{Field: "plan_slug", Code: "PLAN_NOT_FOUND", Message: "must identify an active plan"}}}

		resp := create(api, "web", withKey)

		errs := resp.Diagnostics.Errors()
		withPath, ok := errs[0].(diag.DiagnosticWithPath)
		if len(errs) != 1 || !ok || !withPath.Path().Equal(path.Root("plan_slug")) {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
	})

	t.Run("a field the schema lacks becomes a general error", func(t *testing.T) {
		api := seeded()
		api.createErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED",
			Errors: []client.FieldError{{Field: "subnet_id", Code: "SUBNET_NOT_FOUND", Message: "not found"}}}

		errs := create(api, "web", withKey).Diagnostics.Errors()

		if _, attached := errs[0].(diag.DiagnosticWithPath); attached {
			t.Fatal("a diagnostic on a path the schema does not have would be rejected by Terraform")
		}
	})

	t.Run("a failed order keeps the id in state and tells the user to check the charge", func(t *testing.T) {
		api := seeded()
		failure := "provisioning_handoff_failed"
		api.orderErr = &client.OrderError{Order: client.InstanceOrder{ID: "ord_1", Status: client.OrderFailed, FailureCode: &failure}}

		resp := create(api, "web", withKey)

		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "provisioning_handoff_failed") || !strings.Contains(text, "Check whether the account was charged") {
			t.Fatalf("diags = %s", text)
		}
		if resp.State.Raw.IsNull() || getModel(t, resp.State).ID.ValueString() != "vm_1" {
			t.Fatal("the id must be saved before waiting, so a paid order cannot be orphaned")
		}
		if api.untilWaits != 0 {
			t.Fatal("an instance that never started must not be waited for")
		}
	})

	t.Run("a timeout keeps the id and warns that credit may be reserved", func(t *testing.T) {
		api := seeded()
		api.orderErr = context.DeadlineExceeded

		resp := create(api, "web", withKey)

		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "terraform refresh") || !strings.Contains(text, "credit may already be reserved") || getModel(t, resp.State).ID.ValueString() != "vm_1" {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("an instance that fails to start reports the reason", func(t *testing.T) {
		failing := &failedAfterCreate{fakeAPI: seeded()}
		resp := resource.CreateResponse{State: emptyState(s)}

		newTestResource(failing).Create(ctx, resource.CreateRequest{Plan: planFor(s, "", "web", withKey)}, &resp)

		if text := errorText(resp.Diagnostics); !strings.Contains(text, "failed: NO_CAPACITY: no host") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a lost response is adopted and no order is waited for", func(t *testing.T) {
		api := seeded()
		api.createErr = errConnReset
		api.createLands = true

		resp := create(api, "web", withKey)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if getModel(t, resp.State).ID.ValueString() != "vm_1" || api.creates != 1 || api.orderWaits != 0 || api.untilWaits != 1 {
			t.Fatalf("creates = %d, order waits = %d, until waits = %d", api.creates, api.orderWaits, api.untilWaits)
		}
	})
}

// failedAfterCreate reports the new instance as failed once it is ordered.
type failedAfterCreate struct{ *fakeAPI }

func (f *failedAfterCreate) GetInstance(ctx context.Context, id string) (*client.Instance, error) {
	inst, err := f.fakeAPI.GetInstance(ctx, id)
	if err == nil {
		inst.ObservedState = client.InstanceFailed
		inst.Failure = &client.InstanceFailure{Code: "NO_CAPACITY", Reason: "no host"}
	}
	return inst, err
}

func TestRead(t *testing.T) {
	s := testSchema(t)
	read := func(api *fakeAPI) resource.ReadResponse {
		resp := resource.ReadResponse{State: stateFor(s)}
		newTestResource(api).Read(ctx, resource.ReadRequest{State: stateFor(s)}, &resp)
		return resp
	}

	t.Run("refreshes and keeps the ssh key", func(t *testing.T) {
		inst := running("vm_1", "web-renamed-outside")
		resp := read(seeded(inst))
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.Name.ValueString() != "web-renamed-outside" || m.SSHKeyID.ValueString() != "sshk_1" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("gone is removed from state", func(t *testing.T) {
		resp := read(seeded())
		if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
	})

	t.Run("a deleted instance that still reads 200 is removed from state", func(t *testing.T) {
		inst := running("vm_1", "web")
		inst.ObservedState = client.InstanceDeleted
		resp := read(seeded(inst))
		if !resp.State.Raw.IsNull() {
			t.Fatal("observed_state deleted means gone, even though the API still returns the instance")
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

func TestUpdate(t *testing.T) {
	s := testSchema(t)
	update := func(api *fakeAPI, planName string) resource.UpdateResponse {
		resp := resource.UpdateResponse{State: stateFor(s)}
		newTestResource(api).Update(ctx, resource.UpdateRequest{
			Plan:  planFor(s, "vm_1", planName, map[string]tftypes.Value{"ssh_key_id": str("sshk_1")}),
			State: stateFor(s),
		}, &resp)
		return resp
	}

	t.Run("renames in place", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		resp := update(api, "web-new")
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.renames != 1 || api.lastRenamed != "web-new" || api.creates != 0 || api.deletes != 0 {
			t.Fatalf("renames = %d, creates = %d, deletes = %d", api.renames, api.creates, api.deletes)
		}
		if getModel(t, resp.State).Name.ValueString() != "web-new" {
			t.Fatal("state must carry the new name")
		}
	})

	t.Run("an unchanged name sends nothing", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		if resp := update(api, "web"); resp.Diagnostics.HasError() || api.renames != 0 {
			t.Fatalf("renames = %d, diags = %v", api.renames, resp.Diagnostics)
		}
	})

	t.Run("a rename error is reported", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		api.renameErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "name", Code: "REQUIRED", Message: "is required"}}}
		if !update(api, "web-new").Diagnostics.HasError() {
			t.Fatal("want an error")
		}
	})

	t.Run("a stuck operation finishes when the instance has the new name", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		api.opStuck = true
		if resp := update(api, "web-new"); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
}

func TestDelete(t *testing.T) {
	s := testSchema(t)
	del := func(api *fakeAPI) resource.DeleteResponse {
		var resp resource.DeleteResponse
		newTestResource(api).Delete(ctx, resource.DeleteRequest{State: stateFor(s)}, &resp)
		return resp
	}

	t.Run("deletes and waits for the deleted state", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		if resp := del(api); resp.Diagnostics.HasError() || api.deletes != 1 {
			t.Fatalf("deletes = %d, diags = %v", api.deletes, resp.Diagnostics)
		}
	})

	t.Run("an instance that stays readable as deleted counts as gone", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		api.opStuck = true
		if resp := del(api); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})

	t.Run("an instance that is gone entirely counts as gone", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		api.deleteHard = true
		api.opStuck = true
		if resp := del(api); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})

	t.Run("an instance that never existed counts as deleted", func(t *testing.T) {
		api := seeded()
		api.deleteErr = client.ErrNotFound
		if resp := del(api); resp.Diagnostics.HasError() {
			t.Fatalf("diags = %v: a failed order leaves an id that never existed", resp.Diagnostics)
		}
	})

	t.Run("a public ip blocks the delete with guidance", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		api.deleteErr = &client.APIError{Status: 409, Code: client.CodeInstanceHasPublicIP}
		text := errorText(del(api).Diagnostics)
		if !strings.Contains(text, "public IP") || !strings.Contains(text, "Release") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a failed delete operation is reported", func(t *testing.T) {
		api := seeded(running("vm_1", "web"))
		api.opErr = &client.OperationError{Operation: client.Operation{ID: "op_x", Kind: "delete_instance", Status: "failed", Failure: &client.OperationFailure{Code: "BUSY", Reason: "x"}}}
		if !strings.Contains(errorText(del(api).Diagnostics), "BUSY") {
			t.Fatal("want the failure code")
		}
	})
}

func TestImportState(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid", "vm_06gg88kvrxt6z7h6js3h1799xm", false},
		{"wrong prefix", "sg_123", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
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

// replaceUnlessImported must never destroy a machine only because the API cannot
// report the SSH key it was created with.
func TestReplaceUnlessImported(t *testing.T) {
	tests := []struct {
		name          string
		state, config types.String
		want          bool
	}{
		{"unchanged", types.StringValue("sshk_1"), types.StringValue("sshk_1"), false},
		{"changed key replaces", types.StringValue("sshk_1"), types.StringValue("sshk_2"), true},
		{"key removed replaces", types.StringValue("sshk_1"), types.StringNull(), true},
		{"imported (no state value) does not replace", types.StringNull(), types.StringValue("sshk_1"), false},
		{"nothing either side", types.StringNull(), types.StringNull(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp stringplanmodifier.RequiresReplaceIfFuncResponse
			replaceUnlessImported(ctx, planmodifier.StringRequest{StateValue: tt.state, ConfigValue: tt.config}, &resp)
			if resp.RequiresReplace != tt.want {
				t.Fatalf("RequiresReplace = %v, want %v", resp.RequiresReplace, tt.want)
			}
		})
	}
}
