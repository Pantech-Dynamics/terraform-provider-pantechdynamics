package publicip

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

var nullStr = tftypes.NewValue(tftypes.String, nil)

func planFor(s schema.Schema, purpose string, instance tftypes.Value) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "network_id": Str("net_1"), "purpose": Str(purpose), "instance_id": instance,
		"network_name": UnknownStr(), "instance_name": UnknownStr(), "address": UnknownStr(), "region": UnknownStr(), "zone": UnknownStr(),
		"observed_state": UnknownStr(), "in_sync": Unknown(tftypes.Bool), "created_at": UnknownStr(), "updated_at": UnknownStr(),
	})
}

func stateFor(s schema.Schema) tfsdk.State { return stateWith(s, Str("vm_1")) }

// stateWith is an active static_nat address pointing at instance (null for
// detached).
func stateWith(s schema.Schema, instance tftypes.Value) tfsdk.State {
	return State(s, map[string]tftypes.Value{
		"id": Str("pip_1"), "network_id": Str("net_1"), "purpose": Str("static_nat"), "instance_id": instance,
		"network_name": Str("main"), "instance_name": Str("web"), "address": Str("203.0.113.9"), "region": Str("af-abj"), "zone": Str("af-abj-2"), "observed_state": Str("active"),
		"in_sync": tftypes.NewValue(tftypes.Bool, true), "created_at": Str("2026-10-04T15:00:00Z"), "updated_at": Str("2026-10-04T15:00:00Z"),
	})
}

// updatePlan plans the address of stateWith with instance_id set to instance.
func updatePlan(s schema.Schema, instance tftypes.Value) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": Str("pip_1"), "network_id": Str("net_1"), "purpose": Str("static_nat"), "instance_id": instance,
		"network_name": Str("main"), "instance_name": UnknownStr(), "address": Str("203.0.113.9"), "region": Str("af-abj"), "zone": Str("af-abj-2"),
		"observed_state": UnknownStr(), "in_sync": Unknown(tftypes.Bool), "created_at": Str("2026-10-04T15:00:00Z"), "updated_at": UnknownStr(),
	})
}

// seededIP is the API's view of the address in stateWith.
func seededIP(instance *string) client.PublicIP {
	return client.PublicIP{
		ID: "pip_1", NetworkID: "net_1", Purpose: "static_nat", InstanceID: instance, Address: ptr("203.0.113.9"),
		DesiredState: "present", ObservedState: "active", InSync: true, CreatedAt: now(), UpdatedAt: now(),
	}
}

func update(t *testing.T, api *fakeAPI, from, to tftypes.Value) *resource.UpdateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.UpdateResponse{State: stateWith(s, from)}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: updatePlan(s, to), State: stateWith(s, from)}, resp)
	return resp
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI, purpose string, instance tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, purpose, instance)}, resp)
	return resp
}

func TestCreateStaticNATSavesTheAddress(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, "static_nat", Str("vm_1"))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "pip_1" || m.Address.ValueString() != "203.0.113.9" || m.InstanceID.ValueString() != "vm_1" || m.Zone.ValueString() != "af-abj-2" {
		t.Errorf("model = %+v", m)
	}
	if api.lastCreate.InstanceID != "vm_1" || api.lastCreate.Purpose != "static_nat" || api.lastCreate.NetworkID != "net_1" {
		t.Errorf("request = %+v", api.lastCreate)
	}
}

func TestCreatePortForwardingHasNoInstance(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, "port_forwarding", nullStr)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if m := getModel(t, resp.State); !m.InstanceID.IsNull() {
		t.Errorf("instance_id = %v, want null", m.InstanceID)
	}
	if api.lastCreate.InstanceID != "" {
		t.Errorf("instance_id sent = %q, want it omitted", api.lastCreate.InstanceID)
	}
}

func TestCreateFailureKeepsTheIDAndHintsAtAPoolShortage(t *testing.T) {
	api := &fakeAPI{
		createState: "failed",
		op:          &client.Operation{ID: "op_pip_1", Kind: "create_public_ip", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "IP_POOL_EXHAUSTED", Reason: "no free address"}},
	}
	resp := create(t, api, "static_nat", Str("vm_1"))
	text := ErrorText(resp.Diagnostics)
	if !strings.Contains(text, "IP_POOL_EXHAUSTED") || !strings.Contains(text, "no free public addresses") {
		t.Fatalf("diagnostics = %q", text)
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "pip_1" {
		t.Error("the id was not saved, so the failed address would be orphaned")
	}
}

func TestCreateRefusedByTheAPIPointsAtTheAttribute(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "instance_id", Code: "INSTANCE_NOT_IN_NETWORK", Message: "wrong network"}}}}
	resp := create(t, api, "static_nat", Str("vm_1"))
	if !strings.Contains(ErrorText(resp.Diagnostics), "INSTANCE_NOT_IN_NETWORK") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestCreateLimitRefusalsCarryHints(t *testing.T) {
	for code, want := range map[string]string{
		client.CodePublicIPLimitExceeded:  "20 by default",
		client.CodeStaticNATLimitExceeded: "port_forwarding",
	} {
		t.Run(code, func(t *testing.T) {
			api := &fakeAPI{createErr: &client.APIError{Status: 403, Code: code, Detail: "limit reached"}}
			resp := create(t, api, "static_nat", Str("vm_1"))
			if text := ErrorText(resp.Diagnostics); !strings.Contains(text, code) || !strings.Contains(text, want) || !IsRemoved(resp.State) {
				t.Fatalf("diagnostics = %q", text)
			}
		})
	}
}

func TestReadRemovesAReleasedAddress(t *testing.T) {
	s := testSchema(t)
	for name, api := range map[string]*fakeAPI{
		"404":     {},
		"deleted": {ips: []client.PublicIP{{ID: "pip_1", ObservedState: "deleted"}}},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ReadResponse{State: stateFor(s)}
			(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
			if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
				t.Errorf("want the address removed from state, diags %v", resp.Diagnostics)
			}
		})
	}
}

func TestDeleteWaitsForTheAddressToBeGone(t *testing.T) {
	s := testSchema(t)
	api := &fakeAPI{ips: []client.PublicIP{{ID: "pip_1", ObservedState: "active"}}, opStuck: true}
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || len(api.ips) != 0 {
		t.Errorf("diags %v, addresses left %d", resp.Diagnostics, len(api.ips))
	}
}

func TestDeleteIsIdempotentAndExplainsRefusals(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		err     error
		wantErr string
	}{
		{"already gone", client.ErrNotFound, ""},
		{"rules remain", &client.APIError{Status: 409, Code: client.CodeInvalidResourceState, Detail: "has rules"}, "port forwarding rules still use it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &resource.DeleteResponse{State: stateFor(s)}
			(&Resource{api: &fakeAPI{deleteErr: tt.err}}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
			if tt.wantErr == "" && resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if tt.wantErr != "" && !strings.Contains(ErrorText(resp.Diagnostics), tt.wantErr) {
				t.Errorf("diagnostics = %q, want %q", ErrorText(resp.Diagnostics), tt.wantErr)
			}
		})
	}
}

func TestImportRejectsAWrongPrefix(t *testing.T) {
	s := testSchema(t)
	for id, wantErr := range map[string]bool{"pip_abc": false, "net_abc": true} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("id %q: error = %v, want %v", id, resp.Diagnostics.HasError(), wantErr)
		}
	}
}

func TestValidateConfigTiesInstanceIDToPurpose(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		purpose tftypes.Value
		inst    tftypes.Value
		wantErr string
	}{
		{"static nat with an instance", Str("static_nat"), Str("vm_1"), ""},
		{"default purpose with an instance", nullStr, Str("vm_1"), ""},
		{"default purpose without an instance is a reservation", nullStr, nullStr, ""},
		{"static nat without an instance is a reservation", Str("static_nat"), nullStr, ""},
		{"port forwarding without an instance", Str("port_forwarding"), nullStr, ""},
		{"port forwarding with an instance", Str("port_forwarding"), Str("vm_1"), "instance_id not allowed"},
		{"load balancer without an instance", Str("load_balancer"), nullStr, ""},
		{"load balancer with an instance", Str("load_balancer"), Str("vm_1"), "instance_id not allowed"},
		{"unknown instance is skipped", Str("static_nat"), UnknownStr(), ""},
		{"unknown purpose is skipped", UnknownStr(), nullStr, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Object(s, map[string]tftypes.Value{"network_id": Str("net_1"), "purpose": tt.purpose, "instance_id": tt.inst})
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: cfg}}, resp)
			text := ErrorText(resp.Diagnostics)
			if tt.wantErr == "" && resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error: %s", text)
			}
			if tt.wantErr != "" && !strings.Contains(text, tt.wantErr) {
				t.Errorf("diagnostics = %q, want %q", text, tt.wantErr)
			}
		})
	}
}

func TestCreateStaticNATWithoutAnInstanceReserves(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, "static_nat", nullStr)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if !m.InstanceID.IsNull() || m.Address.ValueString() != "203.0.113.9" || !m.InSync.ValueBool() {
		t.Errorf("model = %+v", m)
	}
	if api.lastCreate.InstanceID != "" || api.lastCreate.Purpose != "static_nat" {
		t.Errorf("request = %+v, want static_nat with no instance", api.lastCreate)
	}
}

func TestInstanceIDChangesInPlace(t *testing.T) {
	s := testSchema(t)
	modifiers := func(name string) int {
		attr, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a string attribute", name)
		}
		return len(attr.PlanModifiers)
	}
	if n := modifiers("instance_id"); n != 0 {
		t.Fatalf("instance_id has %d plan modifiers; a change must attach or detach, not replace", n)
	}
	for _, name := range []string{"network_id", "purpose"} {
		if modifiers(name) == 0 {
			t.Errorf("%s must still replace the address", name)
		}
	}
}

func TestUpdateAttachesAReservedAddressAndWaitsForSync(t *testing.T) {
	api := &fakeAPI{ips: []client.PublicIP{seededIP(nil)}, syncAfter: 3}
	resp := update(t, api, nullStr, Str("vm_2"))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(api.attaches) != 1 || api.attaches[0] != "vm_2" || api.detaches != 0 || api.creates != 0 || api.deletes != 0 {
		t.Fatalf("attaches %v, detaches %d, creates %d, deletes %d", api.attaches, api.detaches, api.creates, api.deletes)
	}
	if len(api.waitedOps) != 1 || api.waitedOps[0] != "op_move" {
		t.Errorf("waited on %v, want op_move", api.waitedOps)
	}
	m := getModel(t, resp.State)
	if m.InstanceID.ValueString() != "vm_2" || !m.InSync.ValueBool() || m.ID.ValueString() != "pip_1" || m.Address.ValueString() != "203.0.113.9" {
		t.Errorf("model = %+v, want the same address on vm_2, in sync", m)
	}
}

func TestUpdateMovesTheAddressToAnotherInstance(t *testing.T) {
	api := &fakeAPI{ips: []client.PublicIP{seededIP(ptr("vm_1"))}, syncAfter: 1}
	resp := update(t, api, Str("vm_1"), Str("vm_2"))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(api.attaches) != 1 || api.attaches[0] != "vm_2" || api.detaches != 0 {
		t.Fatalf("attaches %v, detaches %d: a move is one attach", api.attaches, api.detaches)
	}
	if m := getModel(t, resp.State); m.InstanceID.ValueString() != "vm_2" {
		t.Errorf("instance_id = %v", m.InstanceID)
	}
}

func TestUpdateRemovingInstanceIDDetaches(t *testing.T) {
	api := &fakeAPI{ips: []client.PublicIP{seededIP(ptr("vm_1"))}, syncAfter: 2}
	resp := update(t, api, Str("vm_1"), nullStr)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.detaches != 1 || len(api.attaches) != 0 || api.deletes != 0 {
		t.Fatalf("detaches %d, attaches %v, deletes %d", api.detaches, api.attaches, api.deletes)
	}
	m := getModel(t, resp.State)
	if !m.InstanceID.IsNull() || !m.InSync.ValueBool() || m.Address.ValueString() != "203.0.113.9" {
		t.Errorf("model = %+v, want the address kept, detached, in sync", m)
	}
}

func TestUpdateWithNothingToChangeSkipsTheOperation(t *testing.T) {
	api := &fakeAPI{ips: []client.PublicIP{seededIP(ptr("vm_1"))}, noOp: true}
	resp := update(t, api, nullStr, Str("vm_1")) // state lagged; the API already points there
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(api.waitedOps) != 0 {
		t.Errorf("waited on %v; an empty operation_id has nothing to follow", api.waitedOps)
	}
	if m := getModel(t, resp.State); m.InstanceID.ValueString() != "vm_1" {
		t.Errorf("instance_id = %v", m.InstanceID)
	}
}

func TestUpdateOnlyTimeoutsCallsNothing(t *testing.T) {
	api := &fakeAPI{ips: []client.PublicIP{seededIP(ptr("vm_1"))}}
	resp := update(t, api, Str("vm_1"), Str("vm_1"))
	if resp.Diagnostics.HasError() || len(api.attaches) != 0 || api.detaches != 0 {
		t.Fatalf("diags %v, attaches %v, detaches %d", resp.Diagnostics, api.attaches, api.detaches)
	}
}

func TestUpdateRefusalsCarryHintsAndKeepState(t *testing.T) {
	tests := []struct {
		name     string
		api      *fakeAPI
		from, to tftypes.Value
		want     []string
	}{
		{"instance already has an address", &fakeAPI{attachErr: &client.APIError{Status: 409, Code: client.CodeInstanceAlreadyHasPublicIP}}, Str("vm_1"), Str("vm_2"),
			[]string{"Error attaching public IP", client.CodeInstanceAlreadyHasPublicIP, "Detach that address"}},
		{"not a static nat address", &fakeAPI{detachErr: &client.APIError{Status: 409, Code: client.CodePublicIPNotStaticNAT}}, Str("vm_1"), nullStr,
			[]string{"Error detaching public IP", client.CodePublicIPNotStaticNAT, "Only a static_nat address"}},
		{"instance in another network", &fakeAPI{attachErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "instance_id", Code: client.FieldCodeInstanceNotInNetwork, Message: "is not in the address's network"}}}}, nullStr, Str("vm_9"),
			[]string{"Error attaching public IP", client.FieldCodeInstanceNotInNetwork}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := update(t, tt.api, tt.from, tt.to)
			text := ErrorText(resp.Diagnostics)
			for _, w := range tt.want {
				if !strings.Contains(text, w) {
					t.Errorf("diagnostics = %q, want %q", text, w)
				}
			}
			if m := getModel(t, resp.State); !m.InstanceID.Equal(getModel(t, stateWith(testSchema(t), tt.from)).InstanceID) {
				t.Errorf("instance_id = %v, want the old value kept", m.InstanceID)
			}
		})
	}
}

func TestUpdateFailedOperationStoresWhatThePlatformReports(t *testing.T) {
	api := &fakeAPI{
		ips:     []client.PublicIP{seededIP(ptr("vm_1"))},
		waitErr: &client.OperationError{Operation: client.Operation{ID: "op_move", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "PROVISIONING_FAILED", Reason: "refused"}}},
	}
	resp := update(t, api, Str("vm_1"), nullStr)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	// The fake applied the detach before the operation failed, so state follows
	// the platform rather than the plan or the old state.
	if m := getModel(t, resp.State); !m.InstanceID.IsNull() {
		t.Errorf("instance_id = %v, want what the API reports", m.InstanceID)
	}
}

func TestReadReportsInSyncAndADetachedAddress(t *testing.T) {
	s := testSchema(t)
	ip := seededIP(nil)
	ip.InSync = false
	api := &fakeAPI{ips: []client.PublicIP{ip}, unsynced: 5}
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if !m.InstanceID.IsNull() || m.InSync.ValueBool() || IsRemoved(resp.State) {
		t.Errorf("model = %+v, want kept, detached, in_sync false", m)
	}
}

func TestCreateRefusedForAnInstanceThatHasAnAddress(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 409, Code: client.CodeInstanceAlreadyHasPublicIP}}
	resp := create(t, api, "static_nat", Str("vm_1"))
	if text := ErrorText(resp.Diagnostics); !strings.Contains(text, "Detach that address") {
		t.Fatalf("diagnostics = %q", text)
	}
}
