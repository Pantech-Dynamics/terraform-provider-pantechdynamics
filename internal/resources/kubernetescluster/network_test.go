package kubernetescluster

import (
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// withNetwork is the stored cluster with the network settings the API reports.
func withNetwork() map[string]tftypes.Value {
	a := stored()
	a["kind"], a["cni_plugin"], a["pod_cidr"] = Str("appliance"), Str("flannel"), Str("10.42.0.0/16")
	a["service_cidr"], a["endpoint_access"] = Str("10.43.0.0/16"), Str("public")
	return a
}

func TestCreateSendsTheNetworkSettings(t *testing.T) {
	api := &fakeAPI{}
	a := args(2)
	a["cni_plugin"], a["pod_cidr"], a["service_cidr"] = Str("calico"), Str("10.60.0.0/16"), Str("10.61.0.0/16")
	a["endpoint_access"], a["kind"] = UnknownStr(), UnknownStr()
	resp := create(t, api, a)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if r := api.lastCreate; r.CNIPlugin != "calico" || r.PodCIDR != "10.60.0.0/16" || r.ServiceCIDR != "10.61.0.0/16" {
		t.Errorf("request = %+v", r)
	}
	if len(api.calls) != 0 {
		t.Errorf("calls = %v, want no endpoint change for an unset endpoint_access", api.calls)
	}
	m := getModel(t, resp.State)
	if m.CNIPlugin.ValueString() != "calico" || m.Kind.ValueString() != "appliance" || m.EndpointAccess.ValueString() != "private" {
		t.Errorf("model = %+v", m)
	}
}

func TestCreateLeavesNetworkDefaultsToTheBackend(t *testing.T) {
	api := &fakeAPI{}
	a := args(2)
	for _, k := range []string{"cni_plugin", "pod_cidr", "service_cidr", "endpoint_access", "kind"} {
		a[k] = UnknownStr()
	}
	resp := create(t, api, a)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if r := api.lastCreate; r.CNIPlugin != "" || r.PodCIDR != "" || r.ServiceCIDR != "" {
		t.Errorf("request = %+v, want the defaults left out", r)
	}
	if m := getModel(t, resp.State); m.PodCIDR.ValueString() != "10.42.0.0/16" {
		t.Errorf("pod_cidr = %v", m.PodCIDR)
	}
}

// The create sends the chosen endpoint access, so a public cluster is
// created public, with no change after it runs; unset is the API's
// default, private.
func TestCreateSendsTheEndpointAccess(t *testing.T) {
	for _, tc := range []struct {
		set  tftypes.Value
		send string
		want string
	}{
		{Str("public"), "public", "public"},
		{Str("dual"), "dual", "dual"},
		{UnknownStr(), "", "private"},
	} {
		api := &fakeAPI{}
		a := args(2)
		a["endpoint_access"] = tc.set
		resp := create(t, api, a)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if api.lastCreate.EndpointAccess != tc.send || len(api.calls) != 0 {
			t.Errorf("%v: create sent %q with calls %v, want %q and no follow-up change", tc.set, api.lastCreate.EndpointAccess, api.calls, tc.send)
		}
		if m := getModel(t, resp.State); m.EndpointAccess.ValueString() != tc.want {
			t.Errorf("%v: endpoint_access = %v, want %s", tc.set, m.EndpointAccess, tc.want)
		}
	}
}

func TestUpdateChangesInPlaceInOrder(t *testing.T) {
	api := seeded()
	resp := updateFrom(t, api, withNetwork(), func(a map[string]tftypes.Value) {
		a["node_plan"], a["cni_plugin"], a["endpoint_access"], a["workers"] = Str("s-4vcpu-8gb"), Str("calico"), Str("dual"), Num(3)
		a["node"], a["node_plan_id"] = Unknown(nodeType), UnknownStr()
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !slices.Equal(api.calls, []string{"resize", "network", "endpoint"}) || api.updates != 1 {
		t.Errorf("calls %v, updates %d", api.calls, api.updates)
	}
	if api.lastResize.NodePlan != "s-4vcpu-8gb" || api.lastNetwork.CNIPlugin != "calico" || api.lastNetwork.PodCIDR != "" {
		t.Errorf("resize %+v, network %+v", api.lastResize, api.lastNetwork)
	}
	m := getModel(t, resp.State)
	if m.NodePlan.ValueString() != "s-4vcpu-8gb" || m.NodePlanID.ValueString() != "plan_2" || m.CNIPlugin.ValueString() != "calico" || m.EndpointAccess.ValueString() != "dual" {
		t.Errorf("model = %+v", m)
	}
}

func TestUpdateAfterImportStoresTheNodePlanWithoutAResize(t *testing.T) {
	api := seeded()
	from := withNetwork()
	from["node_plan"] = tftypes.NewValue(tftypes.String, nil)
	resp := updateFrom(t, api, from, func(a map[string]tftypes.Value) { a["node_plan"] = Str("s-2vcpu-4gb") })
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(api.calls) != 0 {
		t.Errorf("calls = %v, want none", api.calls)
	}
	if m := getModel(t, resp.State); m.NodePlan.ValueString() != "s-2vcpu-4gb" {
		t.Errorf("node_plan = %v", m.NodePlan)
	}
}

func TestPlanMarksTheNodeSizeChangingOnAResize(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		name     string
		from, to tftypes.Value
		unknown  bool
	}{
		{"a new plan", Str("s-2vcpu-4gb"), Str("s-4vcpu-8gb"), true},
		{"the same plan", Str("s-2vcpu-4gb"), Str("s-2vcpu-4gb"), false},
		{"after an import", tftypes.NewValue(tftypes.String, nil), Str("s-4vcpu-8gb"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := stored()
			st["node_plan"] = tt.from
			pl := stored()
			pl["node_plan"] = tt.to
			plan := Plan(s, pl)
			resp := &resource.ModifyPlanResponse{Plan: plan}
			planNodeResize(Ctx, resource.ModifyPlanRequest{State: State(s, st), Plan: plan}, resp)
			var node types.Object
			resp.Diagnostics.Append(resp.Plan.GetAttribute(Ctx, path.Root("node"), &node)...)
			if resp.Diagnostics.HasError() || node.IsUnknown() != tt.unknown {
				t.Errorf("node unknown = %v, want %v (%v)", node.IsUnknown(), tt.unknown, resp.Diagnostics)
			}
		})
	}
}
