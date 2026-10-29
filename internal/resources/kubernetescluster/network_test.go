package kubernetescluster

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
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

// privateCluster is the stored cluster with a private endpoint: no public
// address.
func privateCluster(api *fakeAPI) map[string]tftypes.Value {
	k := &api.clusters[0]
	k.EndpointAccess, k.PublicIP, k.PublicPort = "private", nil, nil
	a := withNetwork()
	a["endpoint_access"] = Str("private")
	a["public_ip"], a["public_port"] = tftypes.NewValue(tftypes.String, nil), tftypes.NewValue(tftypes.Number, nil)
	return a
}

// A switch to public finishes only once the public address exists, and the
// kubeconfig is downloaded again for the new address.
func TestSwitchToPublicWaitsForThePublicAddress(t *testing.T) {
	api := seeded()
	resp := updateFrom(t, api, privateCluster(api), func(a map[string]tftypes.Value) { a["endpoint_access"] = Str("public") })
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.PublicIP.ValueString() != "203.0.113.10" || m.PublicPort.ValueInt64() != 6443 {
		t.Errorf("public address = %v:%v", m.PublicIP, m.PublicPort)
	}
	if api.kubeconfigs != 1 {
		t.Errorf("kubeconfig downloads = %d, want 1 for the new address", api.kubeconfigs)
	}

	api = seeded()
	api.unrouted = true
	resp = updateFrom(t, api, privateCluster(api), func(a map[string]tftypes.Value) { a["endpoint_access"] = Str("public") })
	if !resp.Diagnostics.HasError() || !strings.Contains(ErrorText(resp.Diagnostics), "never settled") {
		t.Errorf("diagnostics = %v, want the wait to go on while there is no public address", resp.Diagnostics)
	}
}

func TestReadDownloadsTheKubeconfigAgainWhenTheAPIServerMoves(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.clusters[0].PublicIP = ptr("203.0.113.99")
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || api.kubeconfigs != 1 {
		t.Fatalf("downloads = %d, diagnostics = %v", api.kubeconfigs, resp.Diagnostics)
	}
	if m := getModel(t, resp.State); m.PublicIP.ValueString() != "203.0.113.99" {
		t.Errorf("public_ip = %v", m.PublicIP)
	}

	// A failed download keeps the old address, so the next refresh tries again.
	api = seeded()
	api.clusters[0].PublicIP, api.kubeconfigErr = ptr("203.0.113.99"), errors.New("boom")
	resp = &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("diagnostics = %v, want one warning", resp.Diagnostics)
	}
	if m := getModel(t, resp.State); m.PublicIP.ValueString() != "203.0.113.10" || m.KubeConfig.ValueString() != testKubeconfig {
		t.Errorf("public_ip = %v, kube_config kept = %v", m.PublicIP, m.KubeConfig.ValueString() == testKubeconfig)
	}
}

func TestReadReportsNodeHealth(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	ready := int64(1)
	api.clusters[0].NodeHealth, api.clusters[0].ReadyNodes, api.clusters[0].NodesCheckedAt = ptr("degraded"), &ready, now()
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.NodeHealth.ValueString() != "degraded" || m.ReadyNodes.ValueInt64() != 1 || m.NodesCheckedAt.ValueString() != "2026-10-07T10:00:00Z" {
		t.Errorf("node health = %v, %v, %v", m.NodeHealth, m.ReadyNodes, m.NodesCheckedAt)
	}
}

// An appliance cluster's pod network is fixed at creation, so changing it
// plans a replacement instead of an in-place change the platform refuses.
func TestNetworkChangeReplacesAnApplianceCluster(t *testing.T) {
	s := testSchema(t)
	for kind, want := range map[string]bool{"appliance": true, "other": false} {
		st := withNetwork()
		st["kind"] = Str(kind)
		pl := withNetwork()
		pl["kind"], pl["pod_cidr"] = Str(kind), Str("10.244.0.0/16")
		req := planmodifier.StringRequest{
			Path: path.Root("pod_cidr"), State: State(s, st), Plan: Plan(s, pl),
			StateValue: types.StringValue("10.42.0.0/16"), PlanValue: types.StringValue("10.244.0.0/16"), ConfigValue: types.StringValue("10.244.0.0/16"),
		}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		networkFixedAtCreation().PlanModifyString(Ctx, req, resp)
		if resp.Diagnostics.HasError() || resp.RequiresReplace != want {
			t.Errorf("kind %s: requires replace = %v, want %v (%v)", kind, resp.RequiresReplace, want, resp.Diagnostics)
		}
	}
}

func TestOverlapRefusalPointsAtTheRange(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "pod_cidr", Code: client.CodeKubernetesPodCIDROverlap, Message: "overlaps"}}}}
	resp := create(t, api, args(2))
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Detail(), "leave pod_cidr unset") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

// A cluster that keeps the version it was created with refuses an
// upgrade; the error says what to do instead.
func TestUpgradeRefusalExplainsTheVersionIsFixed(t *testing.T) {
	api := seeded()
	api.upgradeErr = &client.APIError{Status: 409, Code: client.CodeKubernetesUpgradeNotAvailable, Detail: "cannot be upgraded in place"}
	resp := updateFrom(t, api, withNetwork(), func(a map[string]tftypes.Value) { a["kubernetes_version_id"] = Str("k8sv_next") })
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Detail(), "create a new cluster on the version you need") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

// An appliance cluster's node plan is fixed at creation: changing node_plan
// plans a replacement, never the in-place change the platform refuses. A
// state without node_plan (after an import) is filled in, not replaced.
func TestNodePlanChangeReplacesAnApplianceCluster(t *testing.T) {
	s := testSchema(t)
	for name, tc := range map[string]struct {
		kind  string
		state types.String
		want  bool
	}{
		"appliance":    {"appliance", types.StringValue("developer"), true},
		"other kind":   {"other", types.StringValue("developer"), false},
		"after import": {"appliance", types.StringNull(), false},
	} {
		st := withNetwork()
		st["kind"] = Str(tc.kind)
		pl := withNetwork()
		pl["kind"], pl["node_plan"] = Str(tc.kind), Str("performance")
		req := planmodifier.StringRequest{
			Path: path.Root("node_plan"), State: State(s, st), Plan: Plan(s, pl),
			StateValue: tc.state, PlanValue: types.StringValue("performance"), ConfigValue: types.StringValue("performance"),
		}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		nodePlanFixedAtCreation().PlanModifyString(Ctx, req, resp)
		if resp.Diagnostics.HasError() || resp.RequiresReplace != tc.want {
			t.Errorf("%s: requires replace = %v, want %v (%v)", name, resp.RequiresReplace, tc.want, resp.Diagnostics)
		}
	}
}

func TestReadReportsNodeHealthReason(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	ready := int64(2)
	api.clusters[0].NodeHealth, api.clusters[0].ReadyNodes, api.clusters[0].NodesCheckedAt = ptr("degraded"), &ready, now()
	api.clusters[0].NodeHealthReason = ptr("The cluster's pod range (pod_cidr) overlaps its own network")
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if m := getModel(t, resp.State); m.NodeHealth.ValueString() != "degraded" || !strings.Contains(m.NodeHealthReason.ValueString(), "pod_cidr") {
		t.Errorf("node health = %v, reason %v", m.NodeHealth, m.NodeHealthReason)
	}
}

// A switch between private and public moves the API server, so its address
// and the kubeconfig are known after apply; kept from state, Terraform
// refused the apply as an inconsistent result.
func TestPlanMarksTheEndpointAndKubeconfigOnAnAccessChange(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		name     string
		from, to string
		unknown  bool
	}{
		{"private to public", "private", "public", true},
		{"public to private", "public", "private", true},
		{"unchanged", "public", "public", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := stored()
			st["endpoint_access"] = Str(tt.from)
			pl := stored()
			pl["endpoint_access"] = Str(tt.to)
			plan := Plan(s, pl)
			resp := &resource.ModifyPlanResponse{Plan: plan}
			planEndpointMove(Ctx, resource.ModifyPlanRequest{State: State(s, st), Plan: plan}, resp)
			var endpoint, kubeconfig types.String
			resp.Diagnostics.Append(resp.Plan.GetAttribute(Ctx, path.Root("endpoint"), &endpoint)...)
			resp.Diagnostics.Append(resp.Plan.GetAttribute(Ctx, path.Root("kube_config"), &kubeconfig)...)
			if resp.Diagnostics.HasError() || endpoint.IsUnknown() != tt.unknown || kubeconfig.IsUnknown() != tt.unknown {
				t.Errorf("endpoint unknown = %v, kube_config unknown = %v, want %v (%v)", endpoint.IsUnknown(), kubeconfig.IsUnknown(), tt.unknown, resp.Diagnostics)
			}
		})
	}
}
