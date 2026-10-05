package securitygroup

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
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

func newTestResource(api groupAPI) *Resource { return &Resource{api: api} }

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

// rulesValue builds the rules set from (direction, protocol, port, cidr) tuples.
func rulesValue(s schema.Schema, tuples ...[4]string) tftypes.Value {
	setType, ok := objectType(s).AttributeTypes["rules"].(tftypes.Set)
	if !ok {
		panic("rules is not a set")
	}
	elemType, ok := setType.ElementType.(tftypes.Object)
	if !ok {
		panic("rules elements are not objects")
	}
	elems := make([]tftypes.Value, 0, len(tuples))
	for _, tp := range tuples {
		port := tftypes.NewValue(tftypes.String, nil)
		if tp[2] != "" {
			port = str(tp[2])
		}
		elems = append(elems, tftypes.NewValue(elemType, map[string]tftypes.Value{
			"direction": str(tp[0]), "protocol": str(tp[1]), "port_range": port, "cidr": str(tp[3]),
		}))
	}
	return tftypes.NewValue(setType, elems)
}

var (
	ssh  = [4]string{"ingress", "tcp", "22", "10.0.0.0/8"}
	icmp = [4]string{"ingress", "icmp", "", "0.0.0.0/0"}
	dns  = [4]string{"ingress", "udp", "53", "10.0.0.0/8"}
)

func emptyState(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(objectType(s), nil)}
}

// planFor is what Terraform passes to Create or Update: user values set, computed ones unknown.
func planFor(s schema.Schema, id, name string, rules ...[4]string) tfsdk.Plan {
	set := map[string]tftypes.Value{
		"name": str(name), "rules": rulesValue(s, rules...),
		"observed_state": unknown(), "created_at": unknown(), "updated_at": unknown(),
	}
	if id == "" {
		set["id"] = unknown()
	} else {
		set["id"] = str(id)
	}
	return tfsdk.Plan{Schema: s, Raw: values(s, set)}
}

// stateFor is the stored state of the group sg_1, named "web", holding the ssh rule.
func stateFor(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: values(s, map[string]tftypes.Value{
		"id": str("sg_1"), "name": str("web"), "rules": rulesValue(s, ssh),
		"observed_state": str("active"), "created_at": str("2026-10-03T22:57:03Z"), "updated_at": str("2026-10-03T22:57:03Z"),
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

func seeded(groups ...client.SecurityGroup) *fakeAPI {
	return &fakeAPI{groups: groups, nextID: len(groups)}
}

func group(id, name string, rules ...client.SecurityGroupRule) client.SecurityGroup {
	return client.SecurityGroup{ID: id, Name: name, Rules: rules, DesiredState: "present", ObservedState: "active", CreatedAt: now(), UpdatedAt: now()}
}

var (
	sshRule  = client.SecurityGroupRule{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/8"}
	icmpRule = client.SecurityGroupRule{Direction: "ingress", Protocol: "icmp", PortRange: "", CIDR: "0.0.0.0/0"}
	dnsRule  = client.SecurityGroupRule{Direction: "ingress", Protocol: "udp", PortRange: "53", CIDR: "10.0.0.0/8"}
)

// --- tests ---

func TestSchemaIsValid(t *testing.T) {
	if d := testSchema(t).ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func TestMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	New().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &resp)
	if resp.TypeName != "pantechdynamics_security_group" {
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
		t.Fatalf("a real client must satisfy groupAPI: %v", ok.Diagnostics)
	}
}

func TestCreate(t *testing.T) {
	s := testSchema(t)
	create := func(api *fakeAPI, name string, rules ...[4]string) resource.CreateResponse {
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "", name, rules...)}, &resp)
		return resp
	}

	t.Run("success", func(t *testing.T) {
		api := seeded()
		resp := create(api, "web", ssh, icmp)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "sg_1" || m.ObservedState.ValueString() != "active" || len(m.Rules.Elements()) != 2 || m.CreatedAt.IsNull() {
			t.Fatalf("state = %+v", m)
		}
		if !sameRules(api.lastCreate.Rules, []client.SecurityGroupRule{sshRule, icmpRule}) || api.lastCreate.Rules == nil {
			t.Fatalf("rules sent = %+v", api.lastCreate.Rules)
		}
		for _, r := range api.lastCreate.Rules {
			if r.Protocol == "icmp" && r.PortRange != "" {
				t.Fatalf("an omitted port_range must be sent as the empty string, got %q", r.PortRange)
			}
		}
	})

	t.Run("duplicate name is caught before the slow backend 500", func(t *testing.T) {
		api := seeded(group("sg_7", "web", sshRule))
		resp := create(api, "web", ssh)

		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "already exists") || !strings.Contains(text, "terraform import") || !strings.Contains(text, "sg_7") {
			t.Fatalf("diags = %s", text)
		}
		if api.creates != 0 || !resp.State.Raw.IsNull() {
			t.Fatalf("creates = %d: nothing should be created or saved", api.creates)
		}
	})

	t.Run("listing failure stops the create", func(t *testing.T) {
		api := seeded()
		api.listErr = errConnReset
		resp := create(api, "web", ssh)
		if !resp.Diagnostics.HasError() || api.creates != 0 {
			t.Fatalf("diags = %v, creates = %d", resp.Diagnostics, api.creates)
		}
	})

	t.Run("validation errors point at the attribute and save nothing", func(t *testing.T) {
		api := seeded()
		api.createErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED",
			Errors: []client.FieldError{{Field: "name", Code: "REQUIRED", Message: "is required"}}}

		resp := create(api, "web", ssh)

		errs := resp.Diagnostics.Errors()
		withPath, ok := errs[0].(diag.DiagnosticWithPath)
		if len(errs) != 1 || !ok || !withPath.Path().Equal(path.Root("name")) {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
		if !resp.State.Raw.IsNull() {
			t.Fatal("state must stay empty when the backend rejects the create")
		}
	})

	t.Run("a plain 500 hints at a reused name", func(t *testing.T) {
		api := seeded()
		api.createErr = &client.APIError{Status: 500, Code: "INTERNAL", RequestID: "req_9"}

		text := errorText(create(api, "web", ssh).Diagnostics)

		if !strings.Contains(text, "name was used before") || !strings.Contains(text, "req_9") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a failed operation reports the code but keeps the id in state", func(t *testing.T) {
		api := seeded()
		api.waitErr = &client.OperationError{Operation: client.Operation{ID: "op_sg_1", Kind: "create_security_group", Status: "failed",
			Failure: &client.OperationFailure{Code: "CAPACITY", Reason: "no room"}}}

		resp := create(api, "web", ssh)

		if !strings.Contains(errorText(resp.Diagnostics), "CAPACITY") {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
		if resp.State.Raw.IsNull() || getModel(t, resp.State).ID.ValueString() != "sg_1" {
			t.Fatal("the id must be saved before waiting, so a failure cannot orphan the group")
		}
	})

	t.Run("a timeout keeps the id and tells the user to refresh", func(t *testing.T) {
		api := seeded()
		api.waitErr = context.DeadlineExceeded

		resp := create(api, "web", ssh)

		if !strings.Contains(errorText(resp.Diagnostics), "terraform refresh") || getModel(t, resp.State).ID.ValueString() != "sg_1" {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})

	t.Run("an operation that never completes is fine when the group is active", func(t *testing.T) {
		api := seeded()
		api.opStuck = true
		if resp := create(api, "web", ssh); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
}

func TestRead(t *testing.T) {
	s := testSchema(t)
	read := func(api *fakeAPI) resource.ReadResponse {
		resp := resource.ReadResponse{State: stateFor(s)}
		newTestResource(api).Read(ctx, resource.ReadRequest{State: stateFor(s)}, &resp)
		return resp
	}

	t.Run("refreshes the rules from the API", func(t *testing.T) {
		resp := read(seeded(group("sg_1", "web", sshRule, dnsRule)))
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if n := len(getModel(t, resp.State).Rules.Elements()); n != 2 {
			t.Fatalf("rules = %d, want the two rules found on the backend", n)
		}
	})

	t.Run("deleted outside terraform is removed from state", func(t *testing.T) {
		resp := read(seeded())
		if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
	})

	t.Run("a group in the deleted state is removed from state", func(t *testing.T) {
		g := group("sg_1", "web", sshRule)
		g.ObservedState = "deleted"
		resp := read(seeded(g))
		if !resp.State.Raw.IsNull() {
			t.Fatal("state should be removed")
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
	update := func(api *fakeAPI, planRules ...[4]string) resource.UpdateResponse {
		resp := resource.UpdateResponse{State: stateFor(s)}
		newTestResource(api).Update(ctx, resource.UpdateRequest{
			Plan:  planFor(s, "sg_1", "web", planRules...),
			State: stateFor(s),
		}, &resp)
		return resp
	}

	t.Run("sends the full desired set in place", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		resp := update(api, ssh, dns)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.replaces != 1 || api.creates != 0 || api.deletes != 0 || !sameRules(api.lastReplace, []client.SecurityGroupRule{sshRule, dnsRule}) {
			t.Fatalf("replaces = %d, sent = %+v", api.replaces, api.lastReplace)
		}
		if n := len(getModel(t, resp.State).Rules.Elements()); n != 2 {
			t.Fatalf("rules in state = %d", n)
		}
	})

	t.Run("unchanged rules send nothing", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		resp := update(api, ssh)
		if resp.Diagnostics.HasError() || api.replaces != 0 {
			t.Fatalf("replaces = %d, diags = %v", api.replaces, resp.Diagnostics)
		}
	})

	t.Run("validation error from the backend", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		api.replaceErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "rules", Code: "X", Message: "bad"}}}
		resp := update(api, ssh, dns)
		if !resp.Diagnostics.HasError() {
			t.Fatal("want an error")
		}
	})

	t.Run("a stuck operation finishes when the group holds the new rules", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		api.opStuck = true
		if resp := update(api, ssh, dns); resp.Diagnostics.HasError() {
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

	t.Run("deletes and waits until the group is gone", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		if resp := del(api); resp.Diagnostics.HasError() || api.deletes != 1 || api.waits != 1 {
			t.Fatalf("deletes = %d, waits = %d, diags = %v", api.deletes, api.waits, resp.Diagnostics)
		}
	})

	t.Run("finishes on the group being gone even if the operation is stuck", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		api.opStuck = true // the delete operation stayed "submitted" for over an hour on dev
		if resp := del(api); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})

	t.Run("already gone counts as success", func(t *testing.T) {
		api := seeded()
		api.deleteErr = client.ErrNotFound
		if resp := del(api); resp.Diagnostics.HasError() || api.waits != 0 {
			t.Fatalf("diags = %v, waits = %d", resp.Diagnostics, api.waits)
		}
	})

	t.Run("a group still in use explains what to do", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		api.deleteErr = &client.APIError{Status: 409, Code: client.CodeInvalidResourceState}
		text := errorText(del(api).Diagnostics)
		if !strings.Contains(text, "still in use") || !strings.Contains(text, "another security group") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a group attached to a database explains what to do", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		api.deleteErr = &client.APIError{Status: 409, Code: client.CodeSecurityGroupAttachedToDatabase, Detail: "attached to database db_1"}
		text := errorText(del(api).Diagnostics)
		if !strings.Contains(text, "attached to a database") || !strings.Contains(text, "security_group_ids") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("the default group cannot be deleted", func(t *testing.T) {
		api := seeded(group("sg_1", "default", icmpRule))
		api.deleteErr = &client.APIError{Status: 409, Code: client.CodeDefaultSecurityGroupUndeletable}
		text := errorText(del(api).Diagnostics)
		if !strings.Contains(text, "default security group cannot be deleted") || !strings.Contains(text, "terraform state rm") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a failed delete operation is reported", func(t *testing.T) {
		api := seeded(group("sg_1", "web", sshRule))
		api.waitErr = &client.OperationError{Operation: client.Operation{ID: "op_del", Kind: "delete_security_group", Status: "failed", Failure: &client.OperationFailure{Code: "BUSY", Reason: "x"}}}
		if text := errorText(del(api).Diagnostics); !strings.Contains(text, "BUSY") {
			t.Fatalf("diags = %s", text)
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
		{"valid", "sg_06gg7164bnszv3pgjbtvg1d0vm", false},
		{"wrong prefix", "sshk_123", true},
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
				t.Fatalf("id not set")
			}
		})
	}
}
