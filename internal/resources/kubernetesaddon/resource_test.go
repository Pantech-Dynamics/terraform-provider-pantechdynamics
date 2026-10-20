package kubernetesaddon

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

type fakeAPI struct {
	addons      map[string]bool
	clusterGone bool
	unavailable bool
	toggles     []bool
}

func (f *fakeAPI) ListKubernetesAddons(_ context.Context, clusterID string) ([]client.KubernetesAddon, error) {
	if f.clusterGone {
		return nil, fmt.Errorf("listing: %w", client.ErrNotFound)
	}
	out := []client.KubernetesAddon{}
	for name, on := range f.addons {
		status := client.AddonDisabled
		if on {
			status = client.AddonActive
		}
		if f.unavailable {
			status = client.AddonUnavailable
		}
		out = append(out, client.KubernetesAddon{ClusterID: clusterID, Name: name, Enabled: on, Status: status, Version: "1.0"})
	}
	return out, nil
}

func (f *fakeAPI) ToggleKubernetesAddon(_ context.Context, _, name string, req client.ToggleKubernetesAddonRequest) (*client.OperationReference, error) {
	if f.clusterGone {
		return nil, fmt.Errorf("toggling: %w", client.ErrNotFound)
	}
	f.toggles = append(f.toggles, req.Enabled)
	if f.addons == nil {
		f.addons = map[string]bool{}
	}
	f.addons[name] = req.Enabled
	return &client.OperationReference{OperationID: "op_1"}, nil
}

func (f *fakeAPI) WaitForOperation(context.Context, string, client.DoneCheck) error { return nil }

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	return Wait(ctx, done, false)
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func planned(s schema.Schema, enabled bool) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"cluster_id": Str("k8s_1"), "name": Str("metrics-server"), "enabled": tftypes.NewValue(tftypes.Bool, enabled),
		"id": UnknownStr(), "status": UnknownStr(), "version": UnknownStr(),
	})
}

func TestCreateEnablesAndDeleteDisables(t *testing.T) {
	api := &fakeAPI{}
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planned(s, true)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m model
	resp.State.Get(Ctx, &m)
	if m.ID.ValueString() != "k8s_1/metrics-server" || !m.Enabled.ValueBool() || m.Status.ValueString() != "active" {
		t.Errorf("model = %+v", m)
	}
	del := &resource.DeleteResponse{State: resp.State}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: resp.State}, del)
	if del.Diagnostics.HasError() || len(api.toggles) != 2 || api.toggles[1] {
		t.Errorf("toggles %v, diags %v", api.toggles, del.Diagnostics)
	}
}

func TestDeleteOfAGoneClusterSucceeds(t *testing.T) {
	api := &fakeAPI{clusterGone: true}
	s := testSchema(t)
	st := State(s, map[string]tftypes.Value{"cluster_id": Str("k8s_1"), "name": Str("csi-driver"), "enabled": tftypes.NewValue(tftypes.Bool, true)})
	resp := &resource.DeleteResponse{State: st}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestReadRemovesAGoneCluster(t *testing.T) {
	s := testSchema(t)
	st := State(s, map[string]tftypes.Value{"cluster_id": Str("k8s_1"), "name": Str("csi-driver"), "enabled": tftypes.NewValue(tftypes.Bool, true)})
	resp := &resource.ReadResponse{State: st}
	(&Resource{api: &fakeAPI{clusterGone: true}}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Errorf("removed = %v, diags %v", IsRemoved(resp.State), resp.Diagnostics)
	}
}

func TestImportID(t *testing.T) {
	s := testSchema(t)
	for id, ok := range map[string]bool{"k8s_1/metrics-server": true, "metrics-server": false, "vm_1/x": false} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("%q: diags %v", id, resp.Diagnostics)
		}
	}
}

func TestCreateFailsWhenTheClusterCannotRunTheAddon(t *testing.T) {
	api := &fakeAPI{unavailable: true}
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planned(s, true)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unavailable add-on was reported as on")
	}
}
