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
		"address": UnknownStr(), "region": UnknownStr(), "zone": UnknownStr(),
		"observed_state": UnknownStr(), "created_at": UnknownStr(), "updated_at": UnknownStr(),
	})
}

func stateFor(s schema.Schema) tfsdk.State {
	return State(s, map[string]tftypes.Value{
		"id": Str("pip_1"), "network_id": Str("net_1"), "purpose": Str("static_nat"), "instance_id": Str("vm_1"),
		"address": Str("203.0.113.9"), "region": Str("af-abj"), "zone": Str("af-abj-2"), "observed_state": Str("active"),
		"created_at": Str("2026-10-04T15:00:00Z"), "updated_at": Str("2026-10-04T15:00:00Z"),
	})
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
		{"default purpose without an instance", nullStr, nullStr, "Missing instance_id"},
		{"static nat without an instance", Str("static_nat"), nullStr, "Missing instance_id"},
		{"port forwarding without an instance", Str("port_forwarding"), nullStr, ""},
		{"port forwarding with an instance", Str("port_forwarding"), Str("vm_1"), "instance_id not allowed"},
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
