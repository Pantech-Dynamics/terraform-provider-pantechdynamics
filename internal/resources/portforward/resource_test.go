package portforward

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

var unknownNum = Unknown(tftypes.Number)

// sshForward is the plan for forwarding public port 2222 to the instance's port
// 22, with the ends left for the backend to default.
func sshForward(s schema.Schema) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "public_ip_id": Str("pip_1"), "instance_id": Str("vm_1"), "protocol": Str("tcp"),
		"public_port_start": Num(2222), "public_port_end": unknownNum, "private_port_start": Num(22), "private_port_end": unknownNum,
		"observed_state": UnknownStr(), "created_at": UnknownStr(), "updated_at": UnknownStr(),
	})
}

func stateFor(s schema.Schema) tfsdk.State {
	return State(s, map[string]tftypes.Value{
		"id": Str("pfr_1"), "public_ip_id": Str("pip_1"), "instance_id": Str("vm_1"), "protocol": Str("tcp"),
		"public_port_start": Num(2222), "public_port_end": Num(2222), "private_port_start": Num(22), "private_port_end": Num(22),
		"observed_state": Str("active"), "created_at": Str("2026-10-04T15:00:00Z"), "updated_at": Str("2026-10-04T15:00:00Z"),
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

func create(t *testing.T, api *fakeAPI) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: sshForward(s)}, resp)
	return resp
}

func TestCreateSavesTheRuleWithDefaultedEnds(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "pfr_1" || m.PublicPortEnd.ValueInt64() != 2222 || m.PrivatePortStart.ValueInt64() != 22 || m.PrivatePortEnd.ValueInt64() != 22 {
		t.Errorf("model = %+v", m)
	}
	if api.lastIPID != "pip_1" || api.lastCreate.PublicPortEnd != nil || api.lastCreate.PrivatePortEnd != nil || *api.lastCreate.PrivatePortStart != 22 {
		t.Errorf("request = %+v on %s, want unset ends omitted", api.lastCreate, api.lastIPID)
	}
}

func TestCreateFailureKeepsTheID(t *testing.T) {
	api := &fakeAPI{
		createState: "failed",
		op:          &client.Operation{ID: "op_pfr_1", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "PORT_OVERLAP", Reason: "range overlaps another rule"}},
	}
	resp := create(t, api)
	if !strings.Contains(ErrorText(resp.Diagnostics), "PORT_OVERLAP") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "pfr_1" {
		t.Error("the id was not saved")
	}
}

func TestCreateRefusedByTheAPIPointsAtTheAttribute(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "instance_id", Code: "INSTANCE_NOT_IN_NETWORK", Message: "wrong network"}}}}
	resp := create(t, api)
	if !strings.Contains(ErrorText(resp.Diagnostics), "INSTANCE_NOT_IN_NETWORK") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestReadRemovesAMissingRule(t *testing.T) {
	s := testSchema(t)
	for name, api := range map[string]*fakeAPI{
		"gone":           {},
		"address gone":   {getErr: client.ErrNotFound},
		"deleted record": {rules: []client.PortForwardingRule{{ID: "pfr_1", PublicIPID: "pip_1", ObservedState: "deleted"}}},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ReadResponse{State: stateFor(s)}
			(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
			if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
				t.Errorf("want the rule removed from state, diags %v", resp.Diagnostics)
			}
		})
	}
}

func TestDeleteWaitsForTheRuleToBeGone(t *testing.T) {
	s := testSchema(t)
	api := &fakeAPI{rules: []client.PortForwardingRule{{ID: "pfr_1", PublicIPID: "pip_1", ObservedState: "active"}}, opStuck: true}
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || len(api.rules) != 0 {
		t.Errorf("diags %v, rules left %d", resp.Diagnostics, len(api.rules))
	}
}

func TestDeleteOfAnAlreadyGoneRuleSucceeds(t *testing.T) {
	s := testSchema(t)
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: &fakeAPI{deleteErr: client.ErrNotFound}}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestImportNeedsPublicIPAndRuleID(t *testing.T) {
	s := testSchema(t)
	for id, wantErr := range map[string]bool{"pip_1/pfr_1": false, "pfr_1": true, "pfr_1/pip_1": true, "pip_1/": true} {
		t.Run(id, func(t *testing.T) {
			resp := &resource.ImportStateResponse{State: EmptyState(s)}
			(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
			if resp.Diagnostics.HasError() != wantErr {
				t.Fatalf("error = %v, want %v", resp.Diagnostics.HasError(), wantErr)
			}
			if !wantErr {
				if m := getModel(t, resp.State); m.PublicIPID.ValueString() != "pip_1" || m.ID.ValueString() != "pfr_1" {
					t.Errorf("model = %+v", m)
				}
			}
		})
	}
}

func TestValidateConfigRejectsBackwardsRanges(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
	}{
		{"single port", map[string]tftypes.Value{"public_port_start": Num(80)}, false},
		{"range", map[string]tftypes.Value{"public_port_start": Num(8000), "public_port_end": Num(8080)}, false},
		{"backwards public", map[string]tftypes.Value{"public_port_start": Num(90), "public_port_end": Num(80)}, true},
		{"backwards private", map[string]tftypes.Value{"public_port_start": Num(80), "private_port_start": Num(90), "private_port_end": Num(80)}, true},
		{"unknown end", map[string]tftypes.Value{"public_port_start": Num(80), "public_port_end": unknownNum}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: Object(s, tt.set)}}, resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Errorf("error = %v, want %v (%s)", resp.Diagnostics.HasError(), tt.wantErr, ErrorText(resp.Diagnostics))
			}
		})
	}
}
