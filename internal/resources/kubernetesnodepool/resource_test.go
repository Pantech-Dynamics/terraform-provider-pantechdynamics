package kubernetesnodepool

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory poolAPI.
type fakeAPI struct {
	pools                []client.KubernetesNodePool
	clusterGone          bool
	createErr            error
	lastCreate           client.CreateKubernetesNodePoolRequest
	lastScale            client.ScaleKubernetesNodePoolRequest
	scales, deletes, ids int
	unavailable          bool
}

func (f *fakeAPI) createStatus() string {
	if f.unavailable {
		return client.NodePoolUnavailable
	}
	return client.NodePoolReady
}

func (f *fakeAPI) observed(workers int64) int64 {
	if f.unavailable {
		return 0
	}
	return workers
}

func (f *fakeAPI) ListKubernetesNodePools(_ context.Context, clusterID string) ([]client.KubernetesNodePool, error) {
	if f.clusterGone {
		return nil, fmt.Errorf("listing: %w", client.ErrNotFound)
	}
	return slices.Clone(f.pools), nil
}

func (f *fakeAPI) CreateKubernetesNodePool(_ context.Context, clusterID string, req client.CreateKubernetesNodePoolRequest) (*client.OperationReference, error) {
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.ids++
	id := fmt.Sprintf("k8spool_%d", f.ids)
	taints := req.Taints
	if taints == nil {
		taints = []client.KubernetesNodePoolTaint{}
	}
	f.pools = append(f.pools, client.KubernetesNodePool{
		ID: id, ClusterID: clusterID, Name: req.Name, NodePlanID: "plan_2", Workers: req.Workers,
		AutoscalingEnabled: req.AutoscalingEnabled, MinWorkers: req.MinWorkers, MaxWorkers: req.MaxWorkers,
		Labels: req.Labels, Taints: taints, Status: f.createStatus(), ObservedWorkers: f.observed(req.Workers),
	})
	return &client.OperationReference{OperationID: "op_1", ResourceID: id}, nil
}

func (f *fakeAPI) ScaleKubernetesNodePool(_ context.Context, _, poolID string, req client.ScaleKubernetesNodePoolRequest) (*client.OperationReference, error) {
	f.scales++
	f.lastScale = req
	for i := range f.pools {
		if f.pools[i].ID == poolID {
			p := &f.pools[i]
			p.Workers, p.AutoscalingEnabled, p.MinWorkers, p.MaxWorkers = req.Workers, req.AutoscalingEnabled, req.MinWorkers, req.MaxWorkers
		}
	}
	return &client.OperationReference{OperationID: "op_2", ResourceID: poolID}, nil
}

func (f *fakeAPI) DeleteKubernetesNodePool(_ context.Context, _, poolID string) (*client.OperationReference, error) {
	f.deletes++
	f.pools = slices.DeleteFunc(f.pools, func(p client.KubernetesNodePool) bool { return p.ID == poolID })
	return &client.OperationReference{OperationID: "op_3", ResourceID: poolID}, nil
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	return Wait(ctx, done, false)
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	if done == nil {
		return nil
	}
	return Wait(ctx, done, false)
}

var autoType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{"enabled": tftypes.Bool, "min_workers": tftypes.Number, "max_workers": tftypes.Number}}

func autoscaling(lo, hi int64) tftypes.Value {
	return tftypes.NewValue(autoType, map[string]tftypes.Value{"enabled": tftypes.NewValue(tftypes.Bool, true), "min_workers": Num(lo), "max_workers": Num(hi)})
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

// planOf is a create plan; the defaults Terraform applies are set by the caller.
func planOf(s schema.Schema, a map[string]tftypes.Value) tfsdk.Plan {
	base := map[string]tftypes.Value{
		"cluster_id": Str("k8s_1"), "name": Str("db"), "node_plan": Str("s-4vcpu-8gb"),
		"id": UnknownStr(), "node_plan_id": UnknownStr(), "created_at": UnknownStr(),
	}
	for k, v := range a {
		base[k] = v
	}
	return Plan(s, base)
}

func createWith(t *testing.T, api *fakeAPI, a map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	plan := planOf(s, a)
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: plan}, resp)
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

func TestCreateWithAutoscalingStartsAtMin(t *testing.T) {
	api := &fakeAPI{}
	resp := createWith(t, api, map[string]tftypes.Value{"workers": Unknown(tftypes.Number), "autoscaling": autoscaling(2, 5)})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if r := api.lastCreate; r.Workers != 2 || !r.AutoscalingEnabled || r.MinWorkers != 2 || r.MaxWorkers != 5 || r.PlanID != "s-4vcpu-8gb" {
		t.Errorf("request = %+v", r)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "k8spool_1" || m.Workers.ValueInt64() != 2 || m.NodePlanID.ValueString() != "plan_2" || m.NodePlan.ValueString() != "s-4vcpu-8gb" {
		t.Errorf("model = %+v", m)
	}
}

func TestReadRemovesAGonePoolOrCluster(t *testing.T) {
	s := testSchema(t)
	for name, api := range map[string]*fakeAPI{"pool": {}, "cluster": {clusterGone: true}} {
		t.Run(name, func(t *testing.T) {
			st := State(s, map[string]tftypes.Value{"id": Str("k8spool_1"), "cluster_id": Str("k8s_1"), "name": Str("db"), "workers": Num(2)})
			resp := &resource.ReadResponse{State: st}
			(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
			if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
				t.Errorf("removed = %v, diags %v", IsRemoved(resp.State), resp.Diagnostics)
			}
		})
	}
}

func TestUpdateSendsTheWholeSize(t *testing.T) {
	api := &fakeAPI{}
	created := createWith(t, api, map[string]tftypes.Value{"workers": Num(2)})
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	s := testSchema(t)
	// Turn the autoscaler on and leave the count to it.
	planState := tfsdk.State{Schema: s, Raw: created.State.Raw.Copy()}
	auto := types.ObjectValueMust(autoscalingAttrTypes, map[string]attr.Value{
		"enabled": types.BoolValue(true), "min_workers": types.Int64Value(3), "max_workers": types.Int64Value(6),
	})
	if d := planState.SetAttribute(Ctx, path.Root("workers"), types.Int64Unknown()); d.HasError() {
		t.Fatal(d)
	}
	if d := planState.SetAttribute(Ctx, path.Root("autoscaling"), auto); d.HasError() {
		t.Fatal(d)
	}
	p := tfsdk.Plan{Schema: s, Raw: planState.Raw}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: p.Raw}}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: p, State: created.State}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if r := api.lastScale; r.Workers != 3 || !r.AutoscalingEnabled || r.MinWorkers != 3 || r.MaxWorkers != 6 {
		t.Errorf("scale = %+v, want the count moved into the new range", r)
	}
}

func TestDeleteWaitsUntilThePoolIsGone(t *testing.T) {
	api := &fakeAPI{}
	created := createWith(t, api, map[string]tftypes.Value{"workers": Num(1)})
	resp := &resource.DeleteResponse{State: created.State}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: created.State}, resp)
	if resp.Diagnostics.HasError() || api.deletes != 1 || len(api.pools) != 0 {
		t.Errorf("deletes %d, pools %d, diags %v", api.deletes, len(api.pools), resp.Diagnostics)
	}
}

func TestImportNeedsClusterAndPool(t *testing.T) {
	s := testSchema(t)
	for id, ok := range map[string]bool{"k8s_1/k8spool_1": true, "k8spool_1": false, "k8s_1/pool": false} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("%q: diags %v", id, resp.Diagnostics)
		}
	}
}

func TestCheckSize(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		a       map[string]tftypes.Value
		wantErr string
	}{
		{"fixed", map[string]tftypes.Value{"workers": Num(2)}, ""},
		{"autoscaled", map[string]tftypes.Value{"autoscaling": autoscaling(1, 4)}, ""},
		{"no size", map[string]tftypes.Value{}, "Missing workers"},
		{"upside-down range", map[string]tftypes.Value{"autoscaling": autoscaling(5, 2)}, "Invalid autoscaling range"},
		{"start outside the range", map[string]tftypes.Value{"workers": Num(9), "autoscaling": autoscaling(1, 4)}, "outside the autoscaling range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m model
			if d := Plan(s, tt.a).Get(Ctx, &m); d.HasError() {
				t.Fatal(d)
			}
			var d diag.Diagnostics
			checkSize(Ctx, m, &d)
			if got := ErrorText(d); (tt.wantErr == "") != (got == "") || !strings.Contains(got, tt.wantErr) {
				t.Errorf("errors = %q, want %q", got, tt.wantErr)
			}
		})
	}
}

func TestCreateWaitsForWorkersAndReportsThem(t *testing.T) {
	api := &fakeAPI{}
	resp := createWith(t, api, map[string]tftypes.Value{"workers": Num(3)})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Status.ValueString() != "ready" || m.Observed.ValueInt64() != 3 {
		t.Errorf("status %s observed %d", m.Status.ValueString(), m.Observed.ValueInt64())
	}
}

func TestCreateOfAPoolThePlatformCannotBuildFails(t *testing.T) {
	api := &fakeAPI{unavailable: true}
	resp := createWith(t, api, map[string]tftypes.Value{"workers": Num(1)})
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unavailable pool was reported as created")
	}
	if m := getModel(t, resp.State); m.ID.ValueString() != "k8spool_1" {
		t.Errorf("the pool's id was not kept in state: %+v", m)
	}
}
