package loadbalancer

import (
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

var setType = tftypes.Set{ElementType: tftypes.String}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func strSet(vals ...string) tftypes.Value {
	out := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		out = append(out, Str(v))
	}
	return tftypes.NewValue(setType, out)
}

// args are the configured arguments, with defaults applied as Terraform plans them.
func args(name, algorithm string, targets tftypes.Value) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"name": Str(name), "public_ip_id": Str("pip_1"), "subnet_id": Str("snet_1"), "algorithm": Str(algorithm),
		"public_port": Num(80), "private_port": Num(8080), "cidr_list": strSet(), "instance_ids": targets,
	}
}

func planFor(s schema.Schema, a map[string]tftypes.Value) tfsdk.Plan {
	for _, k := range []string{"id", "public_ip_address", "network_id", "protocol", "observed_state", "created_at", "updated_at"} {
		a[k] = UnknownStr()
	}
	return Plan(s, a)
}

// stateFor is a stored, active load balancer lb_1 with targets vm_1 and vm_2.
func stateFor(s schema.Schema) tfsdk.State {
	a := args("web", "roundrobin", strSet("vm_1", "vm_2"))
	a["id"], a["public_ip_address"], a["network_id"], a["protocol"] = Str("lb_1"), Str("203.0.113.9"), Str("net_1"), Str("tcp")
	a["observed_state"], a["created_at"], a["updated_at"] = Str("active"), Str("2026-10-07T10:00:00Z"), Str("2026-10-07T10:00:00Z")
	return State(s, a)
}

// seeded holds lb_1 as stateFor describes it.
func seeded() *fakeAPI {
	return &fakeAPI{nextID: 1, lbs: []client.LoadBalancer{{
		ID: "lb_1", Name: "web", PublicIPID: "pip_1", PublicIPAddress: ptr("203.0.113.9"), NetworkID: ptr("net_1"), SubnetID: "snet_1",
		Protocol: "tcp", Algorithm: "roundrobin", PublicPort: 80, PrivatePort: 8080, CIDRList: []string{},
		Members: members([]string{"vm_1", "vm_2"}), DesiredState: "present", ObservedState: "active", InSync: true,
		CreatedAt: now(), UpdatedAt: now(),
	}}}
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func targetsOf(t *testing.T, m model) []string {
	t.Helper()
	var ids []string
	if d := m.InstanceIDs.ElementsAs(Ctx, &ids, false); d.HasError() {
		t.Fatal(d)
	}
	return ids
}

func create(t *testing.T, api *fakeAPI, targets tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, args("web", "roundrobin", targets))}, resp)
	return resp
}

func update(t *testing.T, api *fakeAPI, name, algorithm string, targets tftypes.Value) *resource.UpdateResponse {
	t.Helper()
	s := testSchema(t)
	plan := planFor(s, args(name, algorithm, targets))
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: plan.Raw}}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: stateFor(s)}, resp)
	return resp
}

func TestCreateSavesTheLoadBalancer(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, strSet("vm_2", "vm_1"))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "lb_1" || m.PublicIPAddress.ValueString() != "203.0.113.9" || m.NetworkID.ValueString() != "net_1" ||
		m.Protocol.ValueString() != "tcp" || m.PrivatePort.ValueInt64() != 8080 || len(targetsOf(t, m)) != 2 || len(m.CIDRList.Elements()) != 0 {
		t.Errorf("model = %+v", m)
	}
	if api.lastCreate.CIDRList != nil || *api.lastCreate.PrivatePort != 8080 || len(api.lastCreate.InstanceIDs) != 2 {
		t.Errorf("request = %+v, want the empty allow-list omitted", api.lastCreate)
	}
}

func TestCreateWithoutTargetsOmitsThem(t *testing.T) {
	api := &fakeAPI{}
	if resp := create(t, api, strSet()); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.lastCreate.InstanceIDs != nil {
		t.Errorf("instance_ids = %v, want omitted", api.lastCreate.InstanceIDs)
	}
}

func TestCreateWaitsForTheTargetsToBeApplied(t *testing.T) {
	api := &fakeAPI{outOfSync: true, opStuck: true}
	resp := create(t, api, strSet("vm_1"))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want the wait to continue while the load balancer is out of sync")
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "lb_1" {
		t.Error("the id was not saved")
	}
}

func TestCreateFailureKeepsTheID(t *testing.T) {
	api := &fakeAPI{
		createState: "failed",
		op:          &client.Operation{ID: "op_lb_1", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "PROVISIONING_JOB_FAILED", Reason: "x"}},
	}
	resp := create(t, api, strSet("vm_1"))
	if !strings.Contains(ErrorText(resp.Diagnostics), "PROVISIONING_JOB_FAILED") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "lb_1" {
		t.Error("the id was not saved")
	}
}

func TestCreateRefusalsCarryHints(t *testing.T) {
	tests := map[string]string{
		client.CodePublicIPNotForLoadBalancer: "purpose = \"load_balancer\"",
		client.CodePortInUse:                  "Choose another port",
	}
	for code, want := range tests {
		t.Run(code, func(t *testing.T) {
			resp := create(t, &fakeAPI{createErr: &client.APIError{Status: 409, Code: code}}, strSet())
			if !strings.Contains(ErrorText(resp.Diagnostics), want) || !IsRemoved(resp.State) {
				t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
			}
		})
	}
}

func TestCreateFieldErrorPointsAtTheAttribute(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "instance_ids", Code: "INSTANCE_NOT_IN_SUBNET", Message: "wrong subnet"}}}}
	resp := create(t, api, strSet("vm_9"))
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Detail(), "INSTANCE_NOT_IN_SUBNET") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if withPath, ok := errs[0].(interface{ Path() path.Path }); !ok || !withPath.Path().Equal(path.Root("instance_ids")) {
		t.Errorf("diagnostic is not on instance_ids: %v", errs[0])
	}
}

func TestReadRefreshesAndDetectsDrift(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.lbs[0].Name = "renamed"
	api.lbs[0].Members = append(members([]string{"vm_1"}), client.LoadBalancerMember{InstanceID: "vm_2", DesiredState: "deleted", ObservedState: "deleting"})
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Name.ValueString() != "renamed" || len(targetsOf(t, m)) != 1 {
		t.Errorf("model = %+v, want the rename and the removed target seen", m)
	}
}

func TestReadRemovesAMissingLoadBalancer(t *testing.T) {
	s := testSchema(t)
	deleted := seeded()
	deleted.lbs[0].ObservedState = "deleted"
	for name, api := range map[string]*fakeAPI{"gone": {}, "deleted record": deleted} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ReadResponse{State: stateFor(s)}
			(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
			if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
				t.Errorf("want the load balancer removed from state, diags %v", resp.Diagnostics)
			}
		})
	}
}

func TestUpdateSendsOnlyWhatChanged(t *testing.T) {
	t.Run("targets", func(t *testing.T) {
		api := seeded()
		resp := update(t, api, "web", "roundrobin", strSet("vm_3"))
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if api.lastUpdate.Name != nil || api.lastUpdate.Algorithm != nil || api.lastUpdate.InstanceIDs == nil || len(*api.lastUpdate.InstanceIDs) != 1 {
			t.Errorf("request = %+v", api.lastUpdate)
		}
		if ids := targetsOf(t, getModel(t, resp.State)); len(ids) != 1 || ids[0] != "vm_3" {
			t.Errorf("targets = %v", ids)
		}
	})
	t.Run("name and algorithm", func(t *testing.T) {
		api := seeded()
		resp := update(t, api, "api", "leastconn", strSet("vm_1", "vm_2"))
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if *api.lastUpdate.Name != "api" || *api.lastUpdate.Algorithm != "leastconn" || api.lastUpdate.InstanceIDs != nil {
			t.Errorf("request = %+v", api.lastUpdate)
		}
		if m := getModel(t, resp.State); m.Name.ValueString() != "api" || m.Algorithm.ValueString() != "leastconn" {
			t.Errorf("model = %+v", m)
		}
	})
	t.Run("removing every target sends an empty list", func(t *testing.T) {
		api := seeded()
		if resp := update(t, api, "web", "roundrobin", strSet()); resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if api.lastUpdate.InstanceIDs == nil || len(*api.lastUpdate.InstanceIDs) != 0 {
			t.Errorf("request = %+v", api.lastUpdate)
		}
	})
	t.Run("only timeouts sends nothing", func(t *testing.T) {
		api := seeded()
		if resp := update(t, api, "web", "roundrobin", strSet("vm_1", "vm_2")); resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if api.updates != 0 {
			t.Errorf("updates = %d, want 0", api.updates)
		}
	})
}

func TestUpdateWithNoOperationDoesNotWait(t *testing.T) {
	api := seeded()
	api.noOpUpdate, api.opStuck = true, true
	if resp := update(t, api, "api", "roundrobin", strSet("vm_1", "vm_2")); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestUpdateRefusedKeepsTheOldState(t *testing.T) {
	api := seeded()
	api.updateErr = &client.APIError{Status: 409, Code: client.CodeLoadBalancerNotChangeable}
	resp := update(t, api, "api", "roundrobin", strSet("vm_1", "vm_2"))
	if !strings.Contains(ErrorText(resp.Diagnostics), "-replace") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
	if m := getModel(t, resp.State); m.Name.ValueString() != "web" || m.ObservedState.IsUnknown() {
		t.Errorf("state = %+v, want the old state", m)
	}
}

func TestDeleteWaitsForTheLoadBalancerToBeGone(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.opStuck = true
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || len(api.lbs) != 0 {
		t.Errorf("diags %v, load balancers left %d", resp.Diagnostics, len(api.lbs))
	}
}

func TestDeleteOfAnAlreadyGoneLoadBalancerSucceeds(t *testing.T) {
	s := testSchema(t)
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: &fakeAPI{deleteErr: client.ErrNotFound}}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestImportChecksThePrefix(t *testing.T) {
	s := testSchema(t)
	for id, wantErr := range map[string]bool{"lb_1": false, "pip_1": true, "": true} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("id %q: error = %v, want %v", id, resp.Diagnostics.HasError(), wantErr)
		}
	}
}

func TestPrivatePortDefaultsToPublicPort(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name   string
		config types.Int64
		public tftypes.Value
		want   types.Int64
	}{
		{"unset follows public_port", types.Int64Null(), Num(443), types.Int64Value(443)},
		{"unset with unknown public_port", types.Int64Null(), Unknown(tftypes.Number), types.Int64Unknown()},
		{"set is kept", types.Int64Value(8443), Num(443), types.Int64Unknown()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Plan(s, map[string]tftypes.Value{"public_port": tt.public})
			req := planmodifier.Int64Request{
				Path: path.Root("private_port"), Plan: plan,
				ConfigValue: tt.config, PlanValue: types.Int64Unknown(),
			}
			resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
			privatePortDefault{}.PlanModifyInt64(Ctx, req, resp)
			if resp.Diagnostics.HasError() || !resp.PlanValue.Equal(tt.want) {
				t.Errorf("plan = %v, want %v (%v)", resp.PlanValue, tt.want, resp.Diagnostics)
			}
		})
	}
}

func TestSetValidators(t *testing.T) {
	many := func(n int, f func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	tests := []struct {
		name    string
		v       validator.Set
		vals    []string
		wantErr string
	}{
		{"cidrs ok", cidrListValidator{}, []string{"0.0.0.0/0", "10.0.0.0/8"}, ""},
		{"cidr host bits", cidrListValidator{}, []string{"10.0.0.1/8"}, "Invalid CIDR"},
		{"cidr ipv6", cidrListValidator{}, []string{"2001:db8::/32"}, "Invalid CIDR"},
		{"too many cidrs", cidrListValidator{}, many(21, func(i int) string { return "10.0." + strconv.Itoa(i) + ".0/24" }), "Too many CIDRs"},
		{"targets ok", instanceIDsValidator{}, []string{"vm_1"}, ""},
		{"target prefix", instanceIDsValidator{}, []string{"pip_1"}, "Invalid instance id"},
		{"too many targets", instanceIDsValidator{}, many(51, func(i int) string { return "vm_" + strconv.Itoa(i) }), "Too many targets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, d := types.SetValueFrom(Ctx, types.StringType, tt.vals)
			if d.HasError() {
				t.Fatal(d)
			}
			resp := &validator.SetResponse{}
			tt.v.ValidateSet(Ctx, validator.SetRequest{Path: path.Root("x"), ConfigValue: set}, resp)
			text := ErrorText(resp.Diagnostics)
			if (tt.wantErr == "") != (text == "") || !strings.Contains(text, tt.wantErr) {
				t.Errorf("errors = %q, want %q", text, tt.wantErr)
			}
		})
	}
}
