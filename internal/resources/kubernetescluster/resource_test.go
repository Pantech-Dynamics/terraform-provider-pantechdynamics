package kubernetescluster

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

var (
	listType = tftypes.List{ElementType: tftypes.String}
	setType  = tftypes.Set{ElementType: tftypes.String}
	nodeType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"vcpu": tftypes.Number, "memory_mb": tftypes.Number, "disk_gb": tftypes.Number,
	}}
	autoType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"enabled": tftypes.Bool, "min_workers": tftypes.Number, "max_workers": tftypes.Number,
	}}
	nullNum = tftypes.NewValue(tftypes.Number, nil)
)

func strSet(vals ...string) tftypes.Value {
	out := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		out = append(out, Str(v))
	}
	return tftypes.NewValue(setType, out)
}

// autoscaling is an autoscaling object; on false leaves the bounds null.
func autoscaling(on bool, lo, hi int64) tftypes.Value {
	if !on {
		return tftypes.NewValue(autoType, map[string]tftypes.Value{"enabled": tftypes.NewValue(tftypes.Bool, false), "min_workers": nullNum, "max_workers": nullNum})
	}
	return tftypes.NewValue(autoType, map[string]tftypes.Value{"enabled": tftypes.NewValue(tftypes.Bool, true), "min_workers": Num(lo), "max_workers": Num(hi)})
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func strList(vals ...string) tftypes.Value {
	out := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		out = append(out, Str(v))
	}
	return tftypes.NewValue(listType, out)
}

// args are the configured arguments, with defaults applied as Terraform plans them.
func args(workers int64) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"name": Str("prod"), "zone_id": Str("af-abj-2"), "kubernetes_version_id": Str("k8sv_1"), "subnet_id": Str("snet_1"),
		"node_plan": Str("s-2vcpu-4gb"), "control_nodes": Num(1), "workers": Num(workers), "api_allowed_cidrs": strSet(),
	}
}

func planFor(s schema.Schema, a map[string]tftypes.Value) tfsdk.Plan {
	for _, k := range []string{"id", "kubernetes_version", "network_id", "node_plan_id", "observed_state", "kube_config", "created_at", "updated_at", "endpoint"} {
		if _, ok := a[k]; !ok {
			a[k] = UnknownStr()
		}
	}
	for k, t := range map[string]tftypes.Type{"node": nodeType, "nodes": tftypes.Number, "in_sync": tftypes.Bool, "available_upgrade_ids": listType, "volume_storage_gb": tftypes.Number} {
		if _, ok := a[k]; !ok {
			a[k] = Unknown(t)
		}
	}
	return Plan(s, a)
}

// stored is the state of the running cluster k8s_1 with 2 workers, as
// seeded() describes it, including its kubeconfig.
func stored() map[string]tftypes.Value {
	a := args(2)
	a["id"], a["kubernetes_version"], a["network_id"], a["node_plan_id"] = Str("k8s_1"), Str("1.31.2"), Str("net_1"), Str("plan_1")
	a["node"] = tftypes.NewValue(nodeType, map[string]tftypes.Value{"vcpu": Num(2), "memory_mb": Num(4096), "disk_gb": Num(40)})
	a["nodes"], a["observed_state"], a["in_sync"] = Num(3), Str("running"), tftypes.NewValue(tftypes.Bool, true)
	a["available_upgrade_ids"], a["kube_config"] = strList("k8sv_2"), Str(testKubeconfig)
	a["created_at"], a["updated_at"] = Str("2026-10-07T10:00:00Z"), Str("2026-10-07T10:00:00Z")
	a["endpoint"], a["volume_storage_gb"] = Str("https://k8s_1.example.test:6443"), Num(30)
	return a
}

// autoscaled is the state of k8s_1 with the autoscaler on (2 to 5) and 4
// workers running.
func autoscaled() map[string]tftypes.Value {
	a := stored()
	a["autoscaling"], a["workers"], a["nodes"] = autoscaling(true, 2, 5), Num(4), Num(5)
	return a
}

// seededAutoscaled holds k8s_1 as autoscaled describes it.
func seededAutoscaled() *fakeAPI {
	api := seeded()
	k := &api.clusters[0]
	k.Autoscaling, k.Workers, k.Nodes = client.KubernetesAutoscaling{Enabled: true, MinWorkers: 2, MaxWorkers: 5}, 4, 5
	return api
}

func stateFor(s schema.Schema) tfsdk.State { return State(s, stored()) }

// seeded holds k8s_1 as stateFor describes it.
func seeded() *fakeAPI {
	return &fakeAPI{nextID: 1, clusters: []client.KubernetesCluster{cluster("k8s_1", 2)}}
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI, a map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, a)}, resp)
	return resp
}

// update applies a plan of version and workers to the stored cluster. Only the
// changed computed values are unknown, as Terraform plans them with
// UseStateForUnknown.
func update(t *testing.T, api *fakeAPI, version string, workers int64) *resource.UpdateResponse {
	t.Helper()
	return updateFrom(t, api, stored(), func(a map[string]tftypes.Value) {
		a["kubernetes_version_id"], a["workers"] = Str(version), Num(workers)
	})
}

// updateFrom applies to the state from a plan that change edits, with the
// computed values Terraform leaves unknown in an update.
func updateFrom(t *testing.T, api *fakeAPI, from map[string]tftypes.Value, change func(map[string]tftypes.Value)) *resource.UpdateResponse {
	t.Helper()
	s := testSchema(t)
	a := map[string]tftypes.Value{}
	for k, v := range from {
		a[k] = v
	}
	change(a)
	for _, k := range []string{"kubernetes_version", "observed_state", "updated_at"} {
		a[k] = UnknownStr()
	}
	a["nodes"], a["in_sync"], a["available_upgrade_ids"], a["volume_storage_gb"] = Unknown(tftypes.Number), Unknown(tftypes.Bool), Unknown(listType), Unknown(tftypes.Number)
	plan := Plan(s, a)
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: plan.Raw}}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: State(s, from)}, resp)
	return resp
}

func TestCreateSavesTheClusterAndItsKubeconfig(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, args(2))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "k8s_1" || m.KubeConfig.ValueString() != testKubeconfig || m.Nodes.ValueInt64() != 3 ||
		m.NetworkID.ValueString() != "net_1" || m.NodePlan.ValueString() != "s-2vcpu-4gb" || !m.InSync.ValueBool() ||
		len(m.AvailableUpgradeIDs.Elements()) != 1 || !m.Node.Attributes()["memory_mb"].Equal(types.Int64Value(4096)) {
		t.Errorf("model = %+v", m)
	}
	if api.lastCreate.SubnetID != "snet_1" || api.lastCreate.ControlNodes != 1 || api.lastCreate.NodePlan != "s-2vcpu-4gb" {
		t.Errorf("request = %+v", api.lastCreate)
	}
}

func TestCreateInAStandardZoneOmitsTheSubnet(t *testing.T) {
	api := &fakeAPI{}
	a := args(1)
	a["zone_id"], a["subnet_id"] = Str("af-abj-1"), tftypes.NewValue(tftypes.String, nil)
	if resp := create(t, api, a); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.lastCreate.SubnetID != "" {
		t.Errorf("subnet_id = %q, want omitted", api.lastCreate.SubnetID)
	}
}

func TestCreateWaitsUntilTheClusterIsInSync(t *testing.T) {
	api := &fakeAPI{outOfSync: true, opStuck: true}
	resp := create(t, api, args(2))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want the wait to continue while the cluster is out of sync")
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "k8s_1" {
		t.Error("the id was not saved")
	}
}

func TestCreateFailureCarriesTheCode(t *testing.T) {
	t.Run("from the operation", func(t *testing.T) {
		api := &fakeAPI{
			createState: "failed",
			op:          &client.Operation{ID: "op_k8s_1", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "PROVISIONING_CAPACITY_UNAVAILABLE", Reason: "x"}},
		}
		resp := create(t, api, args(2))
		text := ErrorText(resp.Diagnostics)
		if !strings.Contains(text, "PROVISIONING_CAPACITY_UNAVAILABLE") || !strings.Contains(text, "Choose a different plan") {
			t.Fatalf("diagnostics = %q", text)
		}
		if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "k8s_1" {
			t.Error("the id was not saved")
		}
	})
	t.Run("from the cluster", func(t *testing.T) {
		api := &fakeAPI{createState: "failed"}
		resp := create(t, api, args(2))
		if !strings.Contains(ErrorText(resp.Diagnostics), `entered the "failed" state`) {
			t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
		}
	})
}

func TestCreateWithoutKubeconfigWarns(t *testing.T) {
	api := &fakeAPI{kubeconfigErr: &client.APIError{Status: 403, Code: "INSUFFICIENT_SCOPE"}}
	resp := create(t, api, args(2))
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("want one warning and no error, got %v", resp.Diagnostics)
	}
	if m := getModel(t, resp.State); !m.KubeConfig.IsNull() || m.ID.ValueString() != "k8s_1" {
		t.Errorf("model = %+v", m)
	}
}

func TestCreateRefusalsCarryHints(t *testing.T) {
	tests := map[string]string{
		client.CodeKubernetesClusterNameTaken: "terraform import",
		client.CodeKubernetesClusterLimit:     "contact support to raise the limit",
		client.CodeInsufficientCredit:         "Top up or add a card",
		client.CodeKubernetesUnavailable:      "not open on this platform yet",
	}
	for code, want := range tests {
		t.Run(code, func(t *testing.T) {
			resp := create(t, &fakeAPI{createErr: &client.APIError{Status: 409, Code: code}}, args(2))
			if !strings.Contains(ErrorText(resp.Diagnostics), want) || !IsRemoved(resp.State) {
				t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
			}
		})
	}
}

func TestCreateFieldErrorPointsAtTheAttributeWithAHint(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "control_nodes", Code: client.CodeKubernetesHANotAvailable, Message: "not offered"}}}}
	resp := create(t, api, args(2))
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Detail(), client.CodeKubernetesHANotAvailable) || !strings.Contains(errs[0].Detail(), "ha_zone_ids") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if withPath, ok := errs[0].(interface{ Path() path.Path }); !ok || !withPath.Path().Equal(path.Root("control_nodes")) {
		t.Errorf("diagnostic is not on control_nodes: %v", errs[0])
	}
}

func TestReadRefreshesWithoutDownloadingTheKubeconfigAgain(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.clusters[0].Workers, api.clusters[0].Nodes = 3, 4
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Workers.ValueInt64() != 3 || m.KubeConfig.ValueString() != testKubeconfig || m.NodePlan.ValueString() != "s-2vcpu-4gb" {
		t.Errorf("model = %+v, want the drift seen and the kubeconfig and plan kept", m)
	}
	if api.kubeconfigs != 0 {
		t.Errorf("kubeconfig downloads = %d, want 0", api.kubeconfigs)
	}
}

func TestReadDownloadsAMissingKubeconfig(t *testing.T) {
	s := testSchema(t)
	a := stored()
	a["kube_config"], a["node_plan"] = tftypes.NewValue(tftypes.String, nil), tftypes.NewValue(tftypes.String, nil) // after an import
	api := seeded()
	resp := &resource.ReadResponse{State: State(s, a)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: State(s, a)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if m := getModel(t, resp.State); m.KubeConfig.ValueString() != testKubeconfig || !m.NodePlan.IsNull() {
		t.Errorf("model = %+v", m)
	}
}

func TestReadRemovesAMissingCluster(t *testing.T) {
	s := testSchema(t)
	deleting := seeded()
	deleting.clusters[0].DesiredState, deleting.clusters[0].ObservedState = "deleted", "stopped"
	retired := seeded()
	retired.clusters[0].ObservedState = "retired"
	for name, api := range map[string]*fakeAPI{"gone": {}, "being deleted": deleting, "retired": retired} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ReadResponse{State: stateFor(s)}
			(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
			if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
				t.Errorf("want the cluster removed from state, diags %v", resp.Diagnostics)
			}
		})
	}
}

func TestReadKeepsAStoppedCluster(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.clusters[0].DesiredState, api.clusters[0].ObservedState = "stopped", "stopped"
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || IsRemoved(resp.State) || getModel(t, resp.State).ObservedState.ValueString() != "stopped" {
		t.Errorf("want the stopped cluster kept, diags %v", resp.Diagnostics)
	}
}

func TestUpdateScalesAndUpgradesInPlace(t *testing.T) {
	t.Run("workers", func(t *testing.T) {
		api := seeded()
		resp := update(t, api, "k8sv_1", 4)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if api.updates != 1 || api.upgrades != 0 || *api.lastUpdate.Workers != 4 || api.lastUpdate.Autoscaling != nil || api.lastUpdate.APIAllowedCIDRs != nil {
			t.Errorf("updates %d, upgrades %d, request %+v", api.updates, api.upgrades, api.lastUpdate)
		}
		if m := getModel(t, resp.State); m.Workers.ValueInt64() != 4 || m.Nodes.ValueInt64() != 5 || m.KubeConfig.ValueString() != testKubeconfig {
			t.Errorf("model = %+v", m)
		}
	})
	t.Run("version", func(t *testing.T) {
		api := seeded()
		resp := update(t, api, "k8sv_2", 2)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if api.upgrades != 1 || api.updates != 0 || api.lastUpgrade.KubernetesVersionID != "k8sv_2" {
			t.Errorf("updates %d, upgrades %d, request %+v", api.updates, api.upgrades, api.lastUpgrade)
		}
		if m := getModel(t, resp.State); m.KubernetesVersion.ValueString() != "1.32.0" || len(m.AvailableUpgradeIDs.Elements()) != 0 {
			t.Errorf("model = %+v", m)
		}
	})
	t.Run("both", func(t *testing.T) {
		api := seeded()
		if resp := update(t, api, "k8sv_2", 3); resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if api.upgrades != 1 || api.updates != 1 {
			t.Errorf("updates %d, upgrades %d", api.updates, api.upgrades)
		}
	})
	t.Run("nothing the API stores", func(t *testing.T) {
		api := seeded()
		if resp := update(t, api, "k8sv_1", 2); resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if api.upgrades != 0 || api.updates != 0 {
			t.Errorf("updates %d, upgrades %d, want none", api.updates, api.upgrades)
		}
	})
}

func TestUpdateWithNoOperationStillChecksTheCluster(t *testing.T) {
	api := seeded()
	api.clusters[0].Workers, api.clusters[0].Nodes = 3, 4 // already scaled outside Terraform: the PATCH changes nothing
	api.opStuck = true
	if resp := update(t, api, "k8sv_1", 3); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.updates != 1 {
		t.Errorf("updates = %d, want 1", api.updates)
	}
}

func TestUpdateRefusedKeepsTheOldState(t *testing.T) {
	api := seeded()
	api.updateErr = &client.APIError{Status: 409, Code: client.CodeKubernetesClusterNotRunning}
	resp := update(t, api, "k8sv_1", 5)
	if !strings.Contains(ErrorText(resp.Diagnostics), "Start it") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
	if m := getModel(t, resp.State); m.Workers.ValueInt64() != 2 || m.ObservedState.IsUnknown() {
		t.Errorf("state = %+v, want the old state", m)
	}
}

func TestUpdateWaitFailureStoresWhatThePlatformReports(t *testing.T) {
	api := seeded()
	api.outOfSync, api.opStuck = true, true
	resp := update(t, api, "k8sv_1", 4)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want the wait to fail")
	}
	if m := getModel(t, resp.State); m.Workers.ValueInt64() != 4 || m.InSync.ValueBool() || m.KubeConfig.ValueString() != testKubeconfig {
		t.Errorf("state = %+v", m)
	}
}

func TestDeleteWaitsUntilTheClusterIsOutOfService(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.opStuck = true
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || api.clusters[0].DesiredState != "deleted" {
		t.Errorf("diags %v, cluster %+v", resp.Diagnostics, api.clusters[0])
	}
}

func TestDeleteOfAnAlreadyGoneClusterSucceeds(t *testing.T) {
	s := testSchema(t)
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: &fakeAPI{deleteErr: client.ErrNotFound}}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestImportChecksThePrefix(t *testing.T) {
	s := testSchema(t)
	for id, wantErr := range map[string]bool{"k8s_1": false, "k8sv_1": true, "lb_1": true, "": true} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("id %q: error = %v, want %v", id, resp.Diagnostics.HasError(), wantErr)
		}
	}
}

func TestPlanRefusesAVersionThatIsNotAnUpgrade(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		version string
		state   tfsdk.State
		wantErr bool
	}{
		{"available upgrade", "k8sv_2", stateFor(s), false},
		{"unchanged", "k8sv_1", stateFor(s), false},
		{"not offered, or a downgrade", "k8sv_0", stateFor(s), true},
		{"create", "k8sv_0", EmptyState(s), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := stored()
			a["kubernetes_version_id"] = Str(tt.version)
			plan := Plan(s, a)
			resp := &resource.ModifyPlanResponse{Plan: plan}
			(&Resource{}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: plan, State: tt.state, Config: tfsdk.Config{Schema: s, Raw: plan.Raw}}, resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Errorf("error = %v, want %v: %v", resp.Diagnostics.HasError(), tt.wantErr, resp.Diagnostics)
			}
			if tt.wantErr && !strings.Contains(ErrorText(resp.Diagnostics), "k8sv_2") {
				t.Errorf("the error does not list the available upgrade: %q", ErrorText(resp.Diagnostics))
			}
		})
	}
}

func TestValidators(t *testing.T) {
	for name, wantErr := range map[string]bool{
		"prod": false, "a": false, "prod-web-1": false, strings.Repeat("a", 40): false,
		strings.Repeat("a", 41): true, "Prod": true, "1prod": true, "prod-": true, "prod_web": true, "": true,
	} {
		resp := &validator.StringResponse{}
		nameValidator{}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("name"), ConfigValue: types.StringValue(name)}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("name %q: error = %v, want %v", name, resp.Diagnostics.HasError(), wantErr)
		}
	}
	for n, wantErr := range map[int64]bool{1: false, 3: false, 2: true, 0: true, 5: true} {
		resp := &validator.Int64Response{}
		controlNodesValidator{}.ValidateInt64(Ctx, validator.Int64Request{Path: path.Root("control_nodes"), ConfigValue: types.Int64Value(n)}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("control_nodes %d: error = %v, want %v", n, resp.Diagnostics.HasError(), wantErr)
		}
	}
}

func autoOf(t *testing.T, m model) autoscalingModel {
	t.Helper()
	var a autoscalingModel
	if m.Autoscaling.IsNull() {
		t.Fatal("autoscaling is null")
	}
	if d := m.Autoscaling.As(Ctx, &a, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatal(d)
	}
	return a
}

func cidrsOf(t *testing.T, m model) []string {
	t.Helper()
	var out []string
	if d := m.APIAllowedCIDRs.ElementsAs(Ctx, &out, false); d.HasError() {
		t.Fatal(d)
	}
	slices.Sort(out)
	return out
}

func TestCreateWithAutoscalingAndAnAllowList(t *testing.T) {
	api := &fakeAPI{}
	a := args(0)
	a["workers"] = Unknown(tftypes.Number) // not configured: the autoscaler owns it
	a["autoscaling"], a["api_allowed_cidrs"] = autoscaling(true, 2, 5), strSet("203.0.113.0/24", "198.51.100.7/32")
	resp := create(t, api, a)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	req := api.lastCreate
	if req.Workers != 0 || req.Autoscaling == nil || *req.Autoscaling != (client.KubernetesAutoscaling{Enabled: true, MinWorkers: 2, MaxWorkers: 5}) || len(req.APIAllowedCIDRs) != 2 {
		t.Errorf("request = %+v", req)
	}
	m := getModel(t, resp.State)
	if au := autoOf(t, m); !au.Enabled.ValueBool() || au.MinWorkers.ValueInt64() != 2 || au.MaxWorkers.ValueInt64() != 5 {
		t.Errorf("autoscaling = %+v", au)
	}
	if m.Workers.ValueInt64() != 2 || !slices.Equal(cidrsOf(t, m), []string{"198.51.100.7/32", "203.0.113.0/24"}) ||
		m.Endpoint.ValueString() != "https://k8s_1.example.test:6443" || m.VolumeStorageGB.ValueInt64() != 30 {
		t.Errorf("model = %+v", m)
	}
}

func TestCreateWithoutAutoscalingSendsWorkersAndNoAllowList(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, args(3))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.lastCreate.Workers != 3 || api.lastCreate.Autoscaling != nil || api.lastCreate.APIAllowedCIDRs != nil {
		t.Errorf("request = %+v", api.lastCreate)
	}
	if m := getModel(t, resp.State); !m.Autoscaling.IsNull() || len(m.APIAllowedCIDRs.Elements()) != 0 {
		t.Errorf("autoscaling %v, cidrs %v; want null and empty", m.Autoscaling, m.APIAllowedCIDRs)
	}
}

func TestCreateOverTheAccountLimitsSaysWhatCounts(t *testing.T) {
	api := &fakeAPI{
		createState: "failed",
		op:          &client.Operation{ID: "op_k8s_1", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "PROVISIONING_LIMIT_EXCEEDED", Reason: "the cluster does not fit your account limits"}},
	}
	resp := create(t, api, args(2))
	text := ErrorText(resp.Diagnostics)
	if !strings.Contains(text, "PROVISIONING_LIMIT_EXCEEDED") || !strings.Contains(text, "max_workers") || !strings.Contains(text, "contact support to raise") {
		t.Fatalf("diagnostics = %q", text)
	}
}

func TestUpdateTurnsAutoscalingOnWithoutSendingWorkers(t *testing.T) {
	api := seeded() // 2 fixed workers
	resp := updateFrom(t, api, stored(), func(a map[string]tftypes.Value) {
		a["autoscaling"], a["workers"] = autoscaling(true, 3, 5), Unknown(tftypes.Number)
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.updates != 1 || api.lastUpdate.Workers != nil || api.lastUpdate.Autoscaling == nil || api.lastUpdate.APIAllowedCIDRs != nil {
		t.Errorf("updates %d, request %+v", api.updates, api.lastUpdate)
	}
	if m := getModel(t, resp.State); m.Workers.ValueInt64() != 3 || autoOf(t, m).MinWorkers.ValueInt64() != 3 {
		t.Errorf("model = %+v, want the count clamped up to min_workers", m)
	}
}

func TestUpdateChangesTheRangeInPlace(t *testing.T) {
	api := seededAutoscaled()
	resp := updateFrom(t, api, autoscaled(), func(a map[string]tftypes.Value) {
		a["autoscaling"], a["workers"] = autoscaling(true, 1, 3), Unknown(tftypes.Number)
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.updates != 1 || api.lastUpdate.Workers != nil || *api.lastUpdate.Autoscaling != (client.KubernetesAutoscaling{Enabled: true, MinWorkers: 1, MaxWorkers: 3}) {
		t.Errorf("request = %+v", api.lastUpdate)
	}
	if m := getModel(t, resp.State); m.Workers.ValueInt64() != 3 || m.Nodes.ValueInt64() != 4 {
		t.Errorf("model = %+v, want 4 workers clamped down to 3", m)
	}
}

func TestUpdateTurnsAutoscalingOffWithAFixedCountInOneRequest(t *testing.T) {
	for name, off := range map[string]tftypes.Value{"removed": tftypes.NewValue(autoType, nil), "enabled = false": autoscaling(false, 0, 0)} {
		t.Run(name, func(t *testing.T) {
			api := seededAutoscaled()
			resp := updateFrom(t, api, autoscaled(), func(a map[string]tftypes.Value) {
				a["autoscaling"], a["workers"] = off, Num(3)
			})
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if api.updates != 1 || api.lastUpdate.Autoscaling == nil || api.lastUpdate.Autoscaling.Enabled || api.lastUpdate.Workers == nil || *api.lastUpdate.Workers != 3 {
				t.Errorf("updates %d, request %+v", api.updates, api.lastUpdate)
			}
			m := getModel(t, resp.State)
			if m.Workers.ValueInt64() != 3 {
				t.Errorf("workers = %v", m.Workers)
			}
			// The state keeps the configuration's shape, or Terraform reports an
			// inconsistent result.
			if off.IsNull() != m.Autoscaling.IsNull() {
				t.Errorf("autoscaling = %v, want the configured shape %v", m.Autoscaling, off)
			}
		})
	}
}

func TestUpdateAllowListAndWorkersShareOnePatch(t *testing.T) {
	api := seeded()
	resp := updateFrom(t, api, stored(), func(a map[string]tftypes.Value) {
		a["workers"], a["api_allowed_cidrs"] = Num(4), strSet("203.0.113.0/24")
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.updates != 1 || *api.lastUpdate.Workers != 4 || !slices.Equal(*api.lastUpdate.APIAllowedCIDRs, []string{"203.0.113.0/24"}) || api.lastUpdate.Autoscaling != nil {
		t.Errorf("updates %d, request %+v", api.updates, api.lastUpdate)
	}
	if m := getModel(t, resp.State); !slices.Equal(cidrsOf(t, m), []string{"203.0.113.0/24"}) || m.Workers.ValueInt64() != 4 {
		t.Errorf("model = %+v", m)
	}
}

func TestUpdateClearingTheAllowListSendsAnEmptyList(t *testing.T) {
	api := seeded()
	api.clusters[0].APIAllowedCIDRs = []string{"203.0.113.0/24"}
	from := stored()
	from["api_allowed_cidrs"] = strSet("203.0.113.0/24")
	resp := updateFrom(t, api, from, func(a map[string]tftypes.Value) { a["api_allowed_cidrs"] = strSet() })
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if got := api.lastUpdate.APIAllowedCIDRs; got == nil || *got == nil || len(*got) != 0 {
		t.Errorf("api_allowed_cidrs sent = %v, want []", got)
	}
	if m := getModel(t, resp.State); len(m.APIAllowedCIDRs.Elements()) != 0 {
		t.Errorf("cidrs = %v", m.APIAllowedCIDRs)
	}
}

func TestUpdateAutoscalingRefusalPointsAtTheAttribute(t *testing.T) {
	api := seeded()
	api.updateErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "autoscaling", Code: client.CodeInvalidKubernetesAutoscaling, Message: "invalid range"}}}
	resp := updateFrom(t, api, stored(), func(a map[string]tftypes.Value) {
		a["autoscaling"], a["workers"] = autoscaling(true, 2, 5), Unknown(tftypes.Number)
	})
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Detail(), "leave workers out") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if withPath, ok := errs[0].(interface{ Path() path.Path }); !ok || !withPath.Path().Equal(path.Root("autoscaling")) {
		t.Errorf("diagnostic is not on autoscaling: %v", errs[0])
	}
	if m := getModel(t, resp.State); !m.Autoscaling.IsNull() || m.Workers.ValueInt64() != 2 {
		t.Errorf("state = %+v, want the old state", m)
	}
}

func TestAllowListRefusalsCarryHints(t *testing.T) {
	for code, want := range map[string]string{
		client.CodeInvalidAPIAllowedCIDRs: "at most 20 IPv4 CIDRs",
		client.CodeAPIAllowedCIDRsVPCOnly: "only for clusters in a VPC zone",
	} {
		t.Run(code, func(t *testing.T) {
			api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "api_allowed_cidrs", Code: code, Message: "refused"}}}}
			a := args(2)
			a["api_allowed_cidrs"] = strSet("203.0.113.0/24")
			resp := create(t, api, a)
			if text := ErrorText(resp.Diagnostics); !strings.Contains(text, want) {
				t.Fatalf("diagnostics = %q", text)
			}
		})
	}
}

func TestReadFollowsTheAutoscaler(t *testing.T) {
	s := testSchema(t)
	api := seededAutoscaled()
	api.clusters[0].Workers, api.clusters[0].Nodes, api.clusters[0].VolumeStorageGB = 5, 6, 80
	resp := &resource.ReadResponse{State: State(s, autoscaled())}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: State(s, autoscaled())}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Workers.ValueInt64() != 5 || m.VolumeStorageGB.ValueInt64() != 80 || autoOf(t, m).MaxWorkers.ValueInt64() != 5 {
		t.Errorf("model = %+v", m)
	}
}

func TestReadShowsAutoscalingTurnedOffOutsideTerraform(t *testing.T) {
	s := testSchema(t)
	api := seeded() // autoscaling off on the platform
	resp := &resource.ReadResponse{State: State(s, autoscaled())}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: State(s, autoscaled())}, resp)
	if m := getModel(t, resp.State); resp.Diagnostics.HasError() || !m.Autoscaling.IsNull() {
		t.Errorf("autoscaling = %v, want null so the plan turns it back on", m.Autoscaling)
	}
}

// modifyPlan runs ModifyPlan with config as Terraform would send it, and
// returns the resulting plan's workers.
func modifyPlan(t *testing.T, state, plan, config map[string]tftypes.Value) types.Int64 {
	t.Helper()
	s := testSchema(t)
	p := Plan(s, plan)
	resp := &resource.ModifyPlanResponse{Plan: p}
	(&Resource{}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: p, State: State(s, state), Config: tfsdk.Config{Schema: s, Raw: Object(s, config)}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var w types.Int64
	resp.Diagnostics.Append(resp.Plan.GetAttribute(Ctx, path.Root("workers"), &w)...)
	return w
}

func TestPlanLeavesAutoscaledWorkersAlone(t *testing.T) {
	config := autoscaled()
	config["workers"] = nullNum
	t.Run("nothing to change: no diff", func(t *testing.T) {
		if w := modifyPlan(t, autoscaled(), autoscaled(), config); w.IsUnknown() || w.ValueInt64() != 4 {
			t.Errorf("workers = %v, want the state's 4", w)
		}
	})
	t.Run("a change: known after apply", func(t *testing.T) {
		plan := autoscaled()
		plan["autoscaling"], plan["updated_at"] = autoscaling(true, 1, 3), UnknownStr()
		cfg := map[string]tftypes.Value{}
		for k, v := range config {
			cfg[k] = v
		}
		cfg["autoscaling"] = autoscaling(true, 1, 3)
		if w := modifyPlan(t, autoscaled(), plan, cfg); !w.IsUnknown() {
			t.Errorf("workers = %v, want unknown: the new range may clamp it", w)
		}
	})
	t.Run("a fixed count stays as configured", func(t *testing.T) {
		plan := stored()
		plan["workers"], plan["updated_at"] = Num(3), UnknownStr()
		if w := modifyPlan(t, stored(), plan, plan); w.ValueInt64() != 3 {
			t.Errorf("workers = %v, want 3", w)
		}
	})
}

func TestDeleteEndsWhenTheClusterAnswersNotFound(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.deleteRemoves, api.opStuck = true, true
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestValidateConfig(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name string
		edit func(a map[string]tftypes.Value)
		want string // substring of the error; empty means valid
	}{
		{"fixed workers", func(map[string]tftypes.Value) {}, ""},
		{"autoscaling without workers", func(a map[string]tftypes.Value) {
			a["autoscaling"], a["workers"] = autoscaling(true, 2, 5), nullNum
		}, ""},
		{"autoscaling off with workers", func(a map[string]tftypes.Value) { a["autoscaling"] = autoscaling(false, 0, 0) }, ""},
		{"allow-list in a VPC zone", func(a map[string]tftypes.Value) { a["api_allowed_cidrs"] = strSet("203.0.113.0/24") }, ""},
		{"unknown autoscaling", func(a map[string]tftypes.Value) { a["autoscaling"], a["workers"] = Unknown(autoType), nullNum }, ""},
		{"workers with autoscaling", func(a map[string]tftypes.Value) { a["autoscaling"] = autoscaling(true, 2, 5) }, "set by the autoscaler"},
		{"neither", func(a map[string]tftypes.Value) { a["workers"] = nullNum }, "Missing workers"},
		{"off without workers", func(a map[string]tftypes.Value) { a["autoscaling"], a["workers"] = autoscaling(false, 0, 0), nullNum }, "Missing workers"},
		{"min above max", func(a map[string]tftypes.Value) { a["autoscaling"], a["workers"] = autoscaling(true, 5, 2), nullNum }, "must not be more than max_workers"},
		{"enabled without bounds", func(a map[string]tftypes.Value) {
			a["workers"] = nullNum
			a["autoscaling"] = tftypes.NewValue(autoType, map[string]tftypes.Value{"enabled": tftypes.NewValue(tftypes.Bool, true), "min_workers": Num(2), "max_workers": nullNum})
		}, "Missing autoscaling bounds"},
		{"bounds while off", func(a map[string]tftypes.Value) {
			a["autoscaling"] = tftypes.NewValue(autoType, map[string]tftypes.Value{"enabled": tftypes.NewValue(tftypes.Bool, false), "min_workers": Num(2), "max_workers": Num(5)})
		}, "only with enabled = true"},
		{"allow-list in a standard zone", func(a map[string]tftypes.Value) {
			a["subnet_id"], a["api_allowed_cidrs"] = tftypes.NewValue(tftypes.String, nil), strSet("203.0.113.0/24")
		}, "needs a VPC zone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := args(2)
			tt.edit(a)
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: Object(s, a)}}, resp)
			text := ErrorText(resp.Diagnostics)
			if (tt.want == "") != (text == "") || !strings.Contains(text, tt.want) {
				t.Errorf("errors = %q, want %q", text, tt.want)
			}
		})
	}
}

func TestCIDRSetValidator(t *testing.T) {
	many := make([]attr.Value, 0, 21)
	for i := range 21 {
		many = append(many, types.StringValue("10.0."+strconv.Itoa(i)+".0/24"))
	}
	tests := map[string]struct {
		in   []attr.Value
		want string
	}{
		"ok":        {[]attr.Value{types.StringValue("203.0.113.0/24"), types.StringValue("198.51.100.7/32")}, ""},
		"empty":     {[]attr.Value{}, ""},
		"host bits": {[]attr.Value{types.StringValue("203.0.113.1/24")}, "Invalid CIDR"},
		"ipv6":      {[]attr.Value{types.StringValue("2001:db8::/32")}, "Invalid CIDR"},
		"bare ip":   {[]attr.Value{types.StringValue("203.0.113.7")}, "Invalid CIDR"},
		"too many":  {many, "Too many CIDRs"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			resp := &validator.SetResponse{}
			cidrSetValidator{}.ValidateSet(Ctx, validator.SetRequest{Path: path.Root("api_allowed_cidrs"), ConfigValue: types.SetValueMust(types.StringType, tt.in)}, resp)
			text := ErrorText(resp.Diagnostics)
			if (tt.want == "") != (text == "") || !strings.Contains(text, tt.want) {
				t.Errorf("errors = %q, want %q", text, tt.want)
			}
		})
	}
}
