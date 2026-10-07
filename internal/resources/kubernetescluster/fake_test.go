package kubernetescluster

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory clusterAPI. A non-nil *Err is returned by the
// matching method.
type fakeAPI struct {
	clusters []client.KubernetesCluster

	createErr, getErr, updateErr, upgradeErr, deleteErr, kubeconfigErr, waitErr error
	createState                                                                 string
	outOfSync                                                                   bool // the cluster never reports in_sync
	opStuck                                                                     bool
	deleteRemoves                                                               bool // GET answers not found right after a delete, as the API does
	op                                                                          *client.Operation

	nextID      int
	updates     int
	upgrades    int
	kubeconfigs int
	lastCreate  client.CreateKubernetesClusterRequest
	lastUpdate  client.UpdateKubernetesClusterRequest
	lastUpgrade client.UpgradeKubernetesClusterRequest
}

const testKubeconfig = "apiVersion: v1\nkind: Config\n"

func now() *time.Time {
	t := time.Date(2026, 10, 7, 10, 0, 0, 123000000, time.UTC)
	return &t
}

func ptr(s string) *string { return &s }

// cluster is a running, in-sync cluster k8s_<n> on version k8sv_1, which can
// be upgraded to k8sv_2.
func cluster(id string, workers int64) client.KubernetesCluster {
	return client.KubernetesCluster{
		ID: id, Name: "prod", ZoneID: "af-abj-2", KubernetesVersionID: "k8sv_1", KubernetesVersion: "1.31.2",
		NetworkID: ptr("net_1"), SubnetID: ptr("snet_1"), NodePlanID: "plan_1",
		Node:         client.KubernetesNode{VCPU: 2, MemoryMB: 4096, DiskGB: 40},
		ControlNodes: 1, Workers: workers, Nodes: 1 + workers,
		DesiredState: "running", ObservedState: "running", InSync: true,
		AvailableUpgrades: []client.KubernetesVersion{{ID: "k8sv_2", ZoneID: "af-abj-2", Version: "1.32.0", Status: "available"}},
		APIAllowedCIDRs:   []string{}, Endpoint: ptr("https://" + id + ".example.test:6443"), VolumeStorageGB: 30,
		CreatedAt: now(), UpdatedAt: now(),
	}
}

func (f *fakeAPI) find(id string) *client.KubernetesCluster {
	for i := range f.clusters {
		if f.clusters[i].ID == id {
			return &f.clusters[i]
		}
	}
	return nil
}

func (f *fakeAPI) CreateKubernetesCluster(_ context.Context, req client.CreateKubernetesClusterRequest) (*client.OperationReference, error) {
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("k8s_%d", f.nextID)
	workers := req.Workers
	if req.Autoscaling != nil && req.Autoscaling.Enabled && workers == 0 {
		workers = req.Autoscaling.MinWorkers // the backend's default
	}
	k := cluster(id, workers)
	k.Name, k.ZoneID, k.KubernetesVersionID = req.Name, req.ZoneID, req.KubernetesVersionID
	if req.ControlNodes != 0 {
		k.ControlNodes = req.ControlNodes
		k.Nodes = req.ControlNodes + workers
	}
	if req.Autoscaling != nil {
		k.Autoscaling = *req.Autoscaling
	}
	if req.APIAllowedCIDRs != nil {
		k.APIAllowedCIDRs = slices.Sorted(slices.Values(req.APIAllowedCIDRs))
	}
	if f.createState != "" {
		k.ObservedState = f.createState
	}
	k.InSync = !f.outOfSync
	f.clusters = append(f.clusters, k)
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id}, nil
}

func (f *fakeAPI) GetKubernetesCluster(_ context.Context, id string) (*client.KubernetesCluster, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if k := f.find(id); k != nil {
		c := *k
		return &c, nil
	}
	return nil, fmt.Errorf("getting Kubernetes cluster: %w", client.ErrNotFound)
}

// UpdateKubernetesCluster applies the PATCH as the backend does: autoscaling
// first, a fixed count refused while it is on, the count clamped into a new
// range, and an empty operation id when nothing changes.
func (f *fakeAPI) UpdateKubernetesCluster(_ context.Context, id string, req client.UpdateKubernetesClusterRequest) (*client.OperationReference, error) {
	f.updates++
	f.lastUpdate = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	k := f.find(id)
	if k == nil {
		return nil, client.ErrNotFound
	}
	auto, workers, cidrs := k.Autoscaling, k.Workers, k.APIAllowedCIDRs
	if req.Autoscaling != nil {
		auto = *req.Autoscaling
	}
	if req.Workers != nil {
		if auto.Enabled {
			return nil, &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "autoscaling", Code: client.CodeInvalidKubernetesAutoscaling}}}
		}
		workers = *req.Workers
	}
	if auto.Enabled {
		workers = min(max(workers, auto.MinWorkers), auto.MaxWorkers)
	}
	if req.APIAllowedCIDRs != nil {
		cidrs = slices.Sorted(slices.Values(*req.APIAllowedCIDRs))
	}
	if auto == k.Autoscaling && workers == k.Workers && slices.Equal(cidrs, k.APIAllowedCIDRs) {
		return &client.OperationReference{ResourceID: id}, nil
	}
	k.Autoscaling, k.Workers, k.Nodes, k.APIAllowedCIDRs = auto, workers, k.ControlNodes+workers, cidrs
	k.InSync = !f.outOfSync
	return &client.OperationReference{OperationID: "op_update", ResourceID: id}, nil
}

func (f *fakeAPI) UpgradeKubernetesCluster(_ context.Context, id string, req client.UpgradeKubernetesClusterRequest) (*client.OperationReference, error) {
	f.upgrades++
	f.lastUpgrade = req
	if f.upgradeErr != nil {
		return nil, f.upgradeErr
	}
	if k := f.find(id); k != nil {
		k.KubernetesVersionID, k.KubernetesVersion, k.AvailableUpgrades = req.KubernetesVersionID, "1.32.0", nil
		k.InSync = !f.outOfSync
	}
	return &client.OperationReference{OperationID: "op_upgrade", ResourceID: id}, nil
}

func (f *fakeAPI) DeleteKubernetesCluster(_ context.Context, id string) (*client.OperationReference, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	// The platform stops and hides the cluster at once; GET then reports it
	// stopped with a deleted intent.
	if k := f.find(id); k != nil {
		k.DesiredState, k.ObservedState = "deleted", "stopped"
	}
	if f.deleteRemoves {
		f.clusters = slices.DeleteFunc(f.clusters, func(c client.KubernetesCluster) bool { return c.ID == id })
	}
	return &client.OperationReference{OperationID: "op_del", ResourceID: id}, nil
}

func (f *fakeAPI) GetKubeconfig(context.Context, string) (string, error) {
	f.kubeconfigs++
	if f.kubeconfigErr != nil {
		return "", f.kubeconfigErr
	}
	return testKubeconfig, nil
}

func (f *fakeAPI) GetOperation(context.Context, string) (*client.Operation, error) {
	if f.op == nil {
		return nil, fmt.Errorf("getting operation: %w", client.ErrNotFound)
	}
	return f.op, nil
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.waitErr != nil {
		return f.waitErr
	}
	return kittest.Wait(ctx, done, f.opStuck)
}

// WaitUntil has no operation to end it, so it fails unless done passes.
func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.waitErr != nil {
		return f.waitErr
	}
	reached, err := done(ctx)
	if err != nil {
		return err
	}
	if !reached {
		return errors.New("the cluster never settled")
	}
	return nil
}
