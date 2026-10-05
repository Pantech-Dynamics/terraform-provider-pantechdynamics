package firewallrule

import (
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

// sshPlan is the plan for an ingress tcp rule on port 22 with the defaults filled in.
func sshPlan(s schema.Schema) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "subnet_id": Str("snet_1"), "number": Num(100), "direction": Str("ingress"), "protocol": Str("tcp"),
		"port_start": Num(22), "port_end": Unknown(tftypes.Number), "cidr": Str("0.0.0.0/0"), "action": Str("allow"),
		"observed_state": UnknownStr(), "created_at": UnknownStr(), "updated_at": UnknownStr(),
	})
}

func stateFor(s schema.Schema) tfsdk.State {
	return State(s, map[string]tftypes.Value{
		"id": Str("aclr_1"), "subnet_id": Str("snet_1"), "number": Num(100), "direction": Str("ingress"), "protocol": Str("tcp"),
		"port_start": Num(22), "port_end": Num(22), "cidr": Str("0.0.0.0/0"), "action": Str("allow"),
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

func create(t *testing.T, api *fakeAPI, plan func(schema.Schema) tfsdk.Plan) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: plan(s)}, resp)
	return resp
}

func TestCreateSavesTheRuleAndTheDefaultedPortEnd(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, sshPlan)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "aclr_1" || m.PortEnd.ValueInt64() != 22 || m.Number.ValueInt64() != 100 || m.Action.ValueString() != "allow" {
		t.Errorf("model = %+v", m)
	}
	if api.lastSubnetID != "snet_1" || api.lastCreate.PortEnd != nil || api.lastCreate.ICMPType != nil {
		t.Errorf("request = %+v on %s, want port_end and icmp fields omitted", api.lastCreate, api.lastSubnetID)
	}
}

func TestCreateAllProtocolHasNoPorts(t *testing.T) {
	plan := func(s schema.Schema) tfsdk.Plan {
		return Plan(s, map[string]tftypes.Value{
			"id": UnknownStr(), "subnet_id": Str("snet_1"), "number": Num(200), "direction": Str("egress"), "protocol": Str("all"),
			"port_end": Unknown(tftypes.Number), "cidr": Str("0.0.0.0/0"), "action": Str("deny"),
			"observed_state": UnknownStr(), "created_at": UnknownStr(), "updated_at": UnknownStr(),
		})
	}
	resp := create(t, &fakeAPI{}, plan)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if !m.PortStart.IsNull() || !m.PortEnd.IsNull() || m.Direction.ValueString() != "egress" || m.Action.ValueString() != "deny" {
		t.Errorf("model = %+v", m)
	}
}

func TestCreateFailureKeepsTheIDAndReportsTheCode(t *testing.T) {
	api := &fakeAPI{
		createState: "failed",
		op:          &client.Operation{ID: "op_aclr_1", Kind: "create_firewall_rule", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "ACL_FAILED", Reason: "the provider refused the rule"}},
	}
	resp := create(t, api, sshPlan)
	if !strings.Contains(ErrorText(resp.Diagnostics), "ACL_FAILED") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "aclr_1" {
		t.Error("the id was not saved")
	}
}

func TestCreateRefusals(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"not a vpc subnet", &client.APIError{Status: 409, Code: codeRulesNotSupported, Detail: "no"}, "pantechdynamics_security_group"},
		{"field error", &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "port_end", Code: "INVALID_PORTS", Message: "bad"}}}, "INVALID_PORTS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := create(t, &fakeAPI{createErr: tt.err}, sshPlan)
			if !strings.Contains(ErrorText(resp.Diagnostics), tt.want) {
				t.Errorf("diagnostics = %q, want %q", ErrorText(resp.Diagnostics), tt.want)
			}
			if !IsRemoved(resp.State) {
				t.Error("nothing was created, so no state should be saved")
			}
		})
	}
}

// Replacing a rule deletes it and adds it again under the same number, and the
// platform holds the number for a few seconds. With no live rule on the number,
// the create must be retried until it is accepted.
func TestCreateRetriesANumberHeldAfterADelete(t *testing.T) {
	api := &fakeAPI{numberTakenTimes: 3}
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api, numberRetryDelay: time.Millisecond}).Create(Ctx, resource.CreateRequest{Plan: sshPlan(s)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.creates != 4 {
		t.Errorf("creates = %d, want 4 (three refusals, then accepted)", api.creates)
	}
	if getModel(t, resp.State).ID.ValueString() != "aclr_1" {
		t.Error("the rule was not saved")
	}
}

// When another live rule really holds the number, retrying cannot help: fail at once.
func TestCreateDoesNotRetryANumberAnotherRuleHolds(t *testing.T) {
	api := &fakeAPI{
		numberTakenTimes: 100,
		rules:            []client.FirewallRule{{ID: "aclr_9", SubnetID: "snet_1", Number: 100, ObservedState: "active"}},
	}
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api, numberRetryDelay: time.Millisecond}).Create(Ctx, resource.CreateRequest{Plan: sshPlan(s)}, resp)

	if !strings.Contains(ErrorText(resp.Diagnostics), "RULE_NUMBER_TAKEN") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
	if api.creates != 1 {
		t.Errorf("creates = %d, want 1: a real conflict must not be retried", api.creates)
	}
	if !IsRemoved(resp.State) {
		t.Error("nothing was created, so no state should be saved")
	}
}

// A number the platform never frees must give up after a bounded number of tries.
func TestCreateGivesUpOnANumberThatNeverFrees(t *testing.T) {
	api := &fakeAPI{numberTakenTimes: 1000}
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api, numberRetryDelay: time.Millisecond}).Create(Ctx, resource.CreateRequest{Plan: sshPlan(s)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if api.creates != numberRetryMax {
		t.Errorf("creates = %d, want %d", api.creates, numberRetryMax)
	}
}

func TestReadRemovesAMissingRule(t *testing.T) {
	s := testSchema(t)
	for name, api := range map[string]*fakeAPI{
		"gone":           {},
		"subnet gone":    {getErr: client.ErrNotFound},
		"deleted record": {rules: []client.FirewallRule{{ID: "aclr_1", SubnetID: "snet_1", ObservedState: "deleted"}}},
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

func TestReadMapsIcmpAnyToNull(t *testing.T) {
	s := testSchema(t)
	anyType := int64(-1)
	api := &fakeAPI{rules: []client.FirewallRule{{ID: "aclr_1", SubnetID: "snet_1", Number: 300, Direction: "ingress", Protocol: "icmp", ICMPType: &anyType, CIDR: "0.0.0.0/0", Action: "allow", ObservedState: "active"}}}
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if m := getModel(t, resp.State); !m.ICMPType.IsNull() || !m.ICMPCode.IsNull() {
		t.Errorf("icmp fields = %v, %v, want null for any", m.ICMPType, m.ICMPCode)
	}
}

func TestDeleteWaitsForTheRuleToBeGone(t *testing.T) {
	s := testSchema(t)
	api := &fakeAPI{rules: []client.FirewallRule{{ID: "aclr_1", SubnetID: "snet_1", ObservedState: "active"}}, opStuck: true}
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

func TestImportNeedsSubnetAndRuleID(t *testing.T) {
	s := testSchema(t)
	tests := map[string]bool{
		"snet_1/aclr_1": false,
		"aclr_1":        true,
		"aclr_1/snet_1": true,
		"snet_1/":       true,
	}
	for id, wantErr := range tests {
		t.Run(id, func(t *testing.T) {
			resp := &resource.ImportStateResponse{State: EmptyState(s)}
			(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
			if resp.Diagnostics.HasError() != wantErr {
				t.Fatalf("error = %v, want %v (%v)", resp.Diagnostics.HasError(), wantErr, resp.Diagnostics)
			}
			if !wantErr {
				m := getModel(t, resp.State)
				if m.SubnetID.ValueString() != "snet_1" || m.ID.ValueString() != "aclr_1" {
					t.Errorf("model = %+v", m)
				}
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr string
	}{
		{"tcp with a port", map[string]tftypes.Value{"protocol": Str("tcp"), "port_start": Num(22)}, ""},
		{"tcp range", map[string]tftypes.Value{"protocol": Str("udp"), "port_start": Num(8000), "port_end": Num(8080)}, ""},
		{"tcp without a port", map[string]tftypes.Value{"protocol": Str("tcp")}, "Missing port_start"},
		{"backwards range", map[string]tftypes.Value{"protocol": Str("tcp"), "port_start": Num(90), "port_end": Num(80)}, "Invalid port range"},
		{"icmp with a port", map[string]tftypes.Value{"protocol": Str("icmp"), "port_start": Num(22)}, "Ports not allowed"},
		{"all with a port end", map[string]tftypes.Value{"protocol": Str("all"), "port_end": Num(22)}, "Ports not allowed"},
		{"tcp with icmp type", map[string]tftypes.Value{"protocol": Str("tcp"), "port_start": Num(22), "icmp_type": Num(8)}, "ICMP field not allowed"},
		{"icmp with type and code", map[string]tftypes.Value{"protocol": Str("icmp"), "icmp_type": Num(8), "icmp_code": Num(0)}, ""},
		{"unknown protocol is skipped", map[string]tftypes.Value{"protocol": UnknownStr()}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: Object(s, tt.set)}}, resp)
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
