package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// KubernetesNodePoolTaint is a Kubernetes taint set on every node of a pool.
// Effect is NoSchedule, PreferNoSchedule or NoExecute.
type KubernetesNodePoolTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// KubernetesNodePool is a group of workers of a cluster with its own plan,
// size, labels and taints. There is no single-pool GET: read it from the list.
type KubernetesNodePool struct {
	ID                 string                    `json:"id"`
	ClusterID          string                    `json:"cluster_id"`
	Name               string                    `json:"name"`
	NodePlanID         string                    `json:"node_plan_id"`
	Workers            int64                     `json:"workers"`
	AutoscalingEnabled bool                      `json:"autoscaling_enabled"`
	MinWorkers         int64                     `json:"min_workers"`
	MaxWorkers         int64                     `json:"max_workers"`
	Labels             map[string]string         `json:"labels"`
	Taints             []KubernetesNodePoolTaint `json:"taints"`
	// Status is provisioning, ready, updating, deleting or unavailable (a
	// pool made while node pools were a preview, with no node size).
	Status string `json:"status"`
	// ObservedWorkers is the pool's workers that have joined the cluster:
	// what is billed.
	ObservedWorkers int64      `json:"observed_workers"`
	CreatedAt       *time.Time `json:"created_at"`
	UpdatedAt       *time.Time `json:"updated_at"`
}

// Node pool statuses the provider branches on.
const (
	NodePoolReady       = "ready"
	NodePoolUnavailable = "unavailable"
)

// CreateKubernetesNodePoolRequest adds a pool. PlanID takes a plan slug or a
// plan_ id. Workers is always sent: the backend requires 1 to 10 even with
// autoscaling on.
type CreateKubernetesNodePoolRequest struct {
	Name               string                    `json:"name"`
	PlanID             string                    `json:"plan_id"`
	Workers            int64                     `json:"workers"`
	AutoscalingEnabled bool                      `json:"autoscaling_enabled,omitempty"`
	MinWorkers         int64                     `json:"min_workers,omitempty"`
	MaxWorkers         int64                     `json:"max_workers,omitempty"`
	Labels             map[string]string         `json:"labels,omitempty"`
	Taints             []KubernetesNodePoolTaint `json:"taints,omitempty"`
}

// ScaleKubernetesNodePoolRequest is the pool's whole new size: the backend
// reads a missing field as zero or false, so every field is always sent.
type ScaleKubernetesNodePoolRequest struct {
	Workers            int64 `json:"workers"`
	AutoscalingEnabled bool  `json:"autoscaling_enabled"`
	MinWorkers         int64 `json:"min_workers"`
	MaxWorkers         int64 `json:"max_workers"`
}

// Add-on statuses: what the cluster runs (installing, active, removing,
// disabled, or unavailable when the cluster has no such component).
const (
	AddonActive      = "active"
	AddonDisabled    = "disabled"
	AddonUnavailable = "unavailable"
)

// KubernetesAddon is a managed add-on of a cluster.
type KubernetesAddon struct {
	ID        string     `json:"id"`
	ClusterID string     `json:"cluster_id"`
	Name      string     `json:"name"`
	Version   string     `json:"version"`
	Status    string     `json:"status"`
	Enabled   bool       `json:"enabled"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// ToggleKubernetesAddonRequest turns an add-on on or off.
type ToggleKubernetesAddonRequest struct {
	Enabled bool `json:"enabled"`
}

// ListKubernetesNodePools returns every pool of a cluster, or ErrNotFound when
// the cluster is gone.
func (c *Client) ListKubernetesNodePools(ctx context.Context, clusterID string) ([]KubernetesNodePool, error) {
	pools, err := listAll[KubernetesNodePool](ctx, c, nodePoolsPath(clusterID))
	if err != nil {
		return nil, fmt.Errorf("listing the node pools of Kubernetes cluster %s: %w", clusterID, err)
	}
	return pools, nil
}

// CreateKubernetesNodePool starts adding a pool. Its id is the ResourceID.
func (c *Client) CreateKubernetesNodePool(ctx context.Context, clusterID string, req CreateKubernetesNodePoolRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, nodePoolsPath(clusterID), req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating node pool in Kubernetes cluster %s: %w", clusterID, err)
	}
	return &ref, nil
}

// ScaleKubernetesNodePool starts resizing a pool to the request's whole size.
func (c *Client) ScaleKubernetesNodePool(ctx context.Context, clusterID, poolID string, req ScaleKubernetesNodePoolRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPatch, nodePoolsPath(clusterID)+"/"+url.PathEscape(poolID), req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("scaling node pool %s: %w", poolID, err)
	}
	return &ref, nil
}

// DeleteKubernetesNodePool starts deleting a pool.
func (c *Client) DeleteKubernetesNodePool(ctx context.Context, clusterID, poolID string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, nodePoolsPath(clusterID)+"/"+url.PathEscape(poolID), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting node pool %s: %w", poolID, err)
	}
	return &ref, nil
}

// ListKubernetesAddons returns a cluster's add-ons, or ErrNotFound when the
// cluster is gone.
func (c *Client) ListKubernetesAddons(ctx context.Context, clusterID string) ([]KubernetesAddon, error) {
	addons, err := listAll[KubernetesAddon](ctx, c, kubernetesClusterPath(clusterID)+"/addons")
	if err != nil {
		return nil, fmt.Errorf("listing the add-ons of Kubernetes cluster %s: %w", clusterID, err)
	}
	return addons, nil
}

// ToggleKubernetesAddon starts turning an add-on on or off.
func (c *Client) ToggleKubernetesAddon(ctx context.Context, clusterID, name string, req ToggleKubernetesAddonRequest) (*OperationReference, error) {
	var ref OperationReference
	path := kubernetesClusterPath(clusterID) + "/addons/" + url.PathEscape(name) + "/toggle"
	if err := c.do(ctx, http.MethodPost, path, req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("turning add-on %s of Kubernetes cluster %s on or off: %w", name, clusterID, err)
	}
	return &ref, nil
}

func nodePoolsPath(clusterID string) string {
	return kubernetesClusterPath(clusterID) + "/node-pools"
}
