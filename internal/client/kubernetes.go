package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the Kubernetes endpoints return.
const (
	// CodeKubernetesUnavailable means managed Kubernetes is not open on this
	// platform yet (503).
	CodeKubernetesUnavailable = "KUBERNETES_UNAVAILABLE"

	// CodeKubernetesClusterNotFound is the 404 for a cluster id that is not in
	// the organization, including a deleted cluster during its 12-hour retire
	// window. It counts as ErrNotFound.
	CodeKubernetesClusterNotFound = "KUBERNETES_CLUSTER_NOT_FOUND"

	// CodeKubernetesClusterNameTaken refuses a create whose name another cluster
	// of the organization already has.
	CodeKubernetesClusterNameTaken = "KUBERNETES_CLUSTER_NAME_TAKEN"

	// CodeKubernetesClusterLimit refuses a create once the organization has as
	// many clusters as it may.
	CodeKubernetesClusterLimit = "KUBERNETES_CLUSTER_LIMIT"

	// CodeKubernetesClusterNotRunning refuses a scale or upgrade of a cluster
	// that is not running.
	CodeKubernetesClusterNotRunning = "KUBERNETES_CLUSTER_NOT_RUNNING"

	// CodeKubernetesClusterBusy refuses a change while another one runs.
	CodeKubernetesClusterBusy = "KUBERNETES_CLUSTER_BUSY"

	// CodeKubernetesClusterNotChangeable refuses a change to a cluster that is
	// stopped, being deleted, or failed.
	CodeKubernetesClusterNotChangeable = "KUBERNETES_CLUSTER_NOT_CHANGEABLE"

	// CodeKubeconfigUnavailable refuses a kubeconfig download before the
	// cluster is running.
	CodeKubeconfigUnavailable = "KUBECONFIG_UNAVAILABLE"

	// CodeSubnetHasKubernetesClusters refuses deleting a subnet that clusters
	// still run in.
	CodeSubnetHasKubernetesClusters = "SUBNET_HAS_KUBERNETES_CLUSTERS"
)

// Field codes a 422 from the Kubernetes endpoints carries in its errors list.
const (
	CodeInvalidKubernetesClusterName  = "INVALID_KUBERNETES_CLUSTER_NAME"
	CodeInvalidKubernetesZone         = "INVALID_KUBERNETES_ZONE"
	CodeInvalidKubernetesVersion      = "INVALID_KUBERNETES_VERSION"
	CodeInvalidKubernetesNodePlan     = "INVALID_KUBERNETES_NODE_PLAN"
	CodeInvalidKubernetesWorkerCount  = "INVALID_KUBERNETES_WORKER_COUNT"
	CodeInvalidKubernetesControlNodes = "INVALID_KUBERNETES_CONTROL_NODES"
	CodeKubernetesHANotAvailable      = "KUBERNETES_HA_NOT_AVAILABLE"
	CodeKubernetesSubnetRequired      = "KUBERNETES_SUBNET_REQUIRED"
	CodeKubernetesSubnetNotAllowed    = "KUBERNETES_SUBNET_NOT_ALLOWED"
	CodeKubernetesSubnetNotFound      = "KUBERNETES_SUBNET_NOT_FOUND"
	CodeKubernetesUpgradeNotAllowed   = "KUBERNETES_UPGRADE_NOT_ALLOWED"
	CodeInvalidKubernetesAutoscaling  = "INVALID_KUBERNETES_AUTOSCALING"
	CodeInvalidAPIAllowedCIDRs        = "INVALID_API_ALLOWED_CIDRS"
	CodeAPIAllowedCIDRsVPCOnly        = "API_ALLOWED_CIDRS_VPC_ONLY"
)

// Cluster observed states the provider branches on.
const (
	KubernetesRunning  = "running"
	KubernetesStopped  = "stopped"
	KubernetesFailed   = "failed"
	KubernetesRetired  = "retired"
	KubernetesDeleted  = "deleted"
	KubernetesUpdating = "updating"
)

// KubernetesVersion is a Kubernetes version clusters can be created with, in
// one zone.
type KubernetesVersion struct {
	ID          string `json:"id"`
	ZoneID      string `json:"zone_id"`
	Version     string `json:"version"`
	Status      string `json:"status"`
	MinCPU      int64  `json:"min_cpu"`
	MinMemoryMB int64  `json:"min_memory_mb"`
}

// KubernetesVersionList is the versions list. It is returned whole and also
// names the zones that offer a highly available control plane.
type KubernetesVersionList struct {
	Data      []KubernetesVersion `json:"data"`
	HAZoneIDs []string            `json:"ha_zone_ids"`
}

// KubernetesNode is the size of every node of a cluster.
type KubernetesNode struct {
	VCPU     int64 `json:"vcpu"`
	MemoryMB int64 `json:"memory_mb"`
	DiskGB   int64 `json:"disk_gb"`
}

// KubernetesAutoscaling is the cluster autoscaler's range. When Enabled is
// false the backend reports (and expects) zero for both bounds, and the worker
// count is fixed.
type KubernetesAutoscaling struct {
	Enabled    bool  `json:"enabled"`
	MinWorkers int64 `json:"min_workers"`
	MaxWorkers int64 `json:"max_workers"`
}

// KubernetesCluster is a managed Kubernetes cluster whose nodes are VMs in the
// account. APIAllowedCIDRs is empty when any address may reach the API server.
// Endpoint is null until the cluster has an API server URL.
// VolumeStorageGB is what the workloads' persistent volume claims use, billed
// with the nodes' disks.
type KubernetesCluster struct {
	ID                  string                `json:"id"`
	Name                string                `json:"name"`
	ZoneID              string                `json:"zone_id"`
	KubernetesVersionID string                `json:"kubernetes_version_id"`
	KubernetesVersion   string                `json:"kubernetes_version"`
	NetworkID           *string               `json:"network_id"`
	SubnetID            *string               `json:"subnet_id"`
	NodePlanID          string                `json:"node_plan_id"`
	Node                KubernetesNode        `json:"node"`
	ControlNodes        int64                 `json:"control_nodes"`
	Workers             int64                 `json:"workers"`
	Nodes               int64                 `json:"nodes"`
	DesiredState        string                `json:"desired_state"`
	ObservedState       string                `json:"observed_state"`
	InSync              bool                  `json:"in_sync"`
	FailureCode         *string               `json:"failure_code"`
	FailureReason       *string               `json:"failure_reason"`
	AvailableUpgrades   []KubernetesVersion   `json:"available_upgrades"`
	Autoscaling         KubernetesAutoscaling `json:"autoscaling"`
	APIAllowedCIDRs     []string              `json:"api_allowed_cidrs"`
	Endpoint            *string               `json:"endpoint"`
	VolumeStorageGB     int64                 `json:"volume_storage_gb"`
	CreatedAt           *time.Time            `json:"created_at"`
	UpdatedAt           *time.Time            `json:"updated_at"`
}

// CreateKubernetesClusterRequest creates a cluster. SubnetID is required in a
// VPC zone and must be left out in a standard zone. A zero ControlNodes is
// omitted, so the backend applies its default of 1. A zero Workers is omitted,
// which the backend allows only with autoscaling enabled (it then starts at
// MinWorkers). APIAllowedCIDRs is VPC zone only; empty is omitted (any address).
type CreateKubernetesClusterRequest struct {
	Name                string                 `json:"name"`
	ZoneID              string                 `json:"zone_id"`
	KubernetesVersionID string                 `json:"kubernetes_version_id"`
	SubnetID            string                 `json:"subnet_id,omitempty"`
	NodePlan            string                 `json:"node_plan"`
	ControlNodes        int64                  `json:"control_nodes,omitempty"`
	Workers             int64                  `json:"workers,omitempty"`
	Autoscaling         *KubernetesAutoscaling `json:"autoscaling,omitempty"`
	APIAllowedCIDRs     []string               `json:"api_allowed_cidrs,omitempty"`
}

// UpdateKubernetesClusterRequest is the PATCH body: a nil field is left out and
// stays as it is. Workers is refused while autoscaling is enabled (the
// autoscaler sets it), unless the same request turns autoscaling off.
// APIAllowedCIDRs, when not nil, replaces the whole list; a pointer to an empty
// slice sends [] and allows any address again.
type UpdateKubernetesClusterRequest struct {
	Workers         *int64                 `json:"workers,omitempty"`
	Autoscaling     *KubernetesAutoscaling `json:"autoscaling,omitempty"`
	APIAllowedCIDRs *[]string              `json:"api_allowed_cidrs,omitempty"`
}

// UpgradeKubernetesClusterRequest moves a cluster to one of its available
// upgrades.
type UpgradeKubernetesClusterRequest struct {
	KubernetesVersionID string `json:"kubernetes_version_id"`
}

// Kubeconfig is a cluster's admin kubeconfig file (YAML). It is a secret.
type Kubeconfig struct {
	Kubeconfig string `json:"kubeconfig"`
}

// ListKubernetesVersions returns the versions clusters can be created with,
// only those in zoneID when it is set, and the zones offering 3 control nodes.
func (c *Client) ListKubernetesVersions(ctx context.Context, zoneID string) (*KubernetesVersionList, error) {
	path := "/kubernetes-versions"
	if zoneID != "" {
		path += "?zone_id=" + url.QueryEscape(zoneID)
	}
	var list KubernetesVersionList
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, fmt.Errorf("listing Kubernetes versions: %w", err)
	}
	return &list, nil
}

// CreateKubernetesCluster starts creating a cluster. Its id is the returned
// ResourceID.
func (c *Client) CreateKubernetesCluster(ctx context.Context, req CreateKubernetesClusterRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, "/kubernetes-clusters", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating Kubernetes cluster: %w", err)
	}
	return &ref, nil
}

// ListKubernetesClusters returns every cluster of the account, following the
// cursor. Deleted and failed ones are left out by the backend.
func (c *Client) ListKubernetesClusters(ctx context.Context) ([]KubernetesCluster, error) {
	clusters, err := listAll[KubernetesCluster](ctx, c, "/kubernetes-clusters")
	if err != nil {
		return nil, fmt.Errorf("listing Kubernetes clusters: %w", err)
	}
	return clusters, nil
}

// GetKubernetesCluster returns one cluster, or ErrNotFound.
func (c *Client) GetKubernetesCluster(ctx context.Context, id string) (*KubernetesCluster, error) {
	var k KubernetesCluster
	if err := c.do(ctx, http.MethodGet, kubernetesClusterPath(id), nil, &k); err != nil {
		return nil, fmt.Errorf("getting Kubernetes cluster %s: %w", id, err)
	}
	return &k, nil
}

// UpdateKubernetesCluster starts changing the worker count, the autoscaler or
// the API server allow-list, whichever the request carries. An empty
// OperationID in the result means nothing changed.
func (c *Client) UpdateKubernetesCluster(ctx context.Context, id string, req UpdateKubernetesClusterRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPatch, kubernetesClusterPath(id), req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("changing Kubernetes cluster %s: %w", id, err)
	}
	return &ref, nil
}

// UpgradeKubernetesCluster starts moving a cluster to a newer version.
func (c *Client) UpgradeKubernetesCluster(ctx context.Context, id string, req UpgradeKubernetesClusterRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, kubernetesClusterPath(id)+"/upgrade", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("upgrading Kubernetes cluster %s: %w", id, err)
	}
	return &ref, nil
}

// StartKubernetesCluster starts a stopped cluster.
func (c *Client) StartKubernetesCluster(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, kubernetesClusterPath(id)+"/start", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("starting Kubernetes cluster %s: %w", id, err)
	}
	return &ref, nil
}

// StopKubernetesCluster stops a running cluster. Only its disks are billed
// while it is stopped.
func (c *Client) StopKubernetesCluster(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, kubernetesClusterPath(id)+"/stop", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("stopping Kubernetes cluster %s: %w", id, err)
	}
	return &ref, nil
}

// DeleteKubernetesCluster starts deleting a cluster. It stops at once and is
// hidden from lists; its nodes and disks are destroyed 12 hours later. An
// empty OperationID means it was already being deleted.
func (c *Client) DeleteKubernetesCluster(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, kubernetesClusterPath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting Kubernetes cluster %s: %w", id, err)
	}
	return &ref, nil
}

// GetKubeconfig downloads the cluster's admin kubeconfig. It is a POST that
// needs a write key, so a read-only key can never fetch it, and it returns
// KUBECONFIG_UNAVAILABLE until the cluster is running. Retrying is safe: it
// changes nothing.
func (c *Client) GetKubeconfig(ctx context.Context, id string) (string, error) {
	var kc Kubeconfig
	if err := c.do(ctx, http.MethodPost, kubernetesClusterPath(id)+"/kubeconfig", nil, &kc, replaySafe()); err != nil {
		return "", fmt.Errorf("downloading the kubeconfig of Kubernetes cluster %s: %w", id, err)
	}
	return kc.Kubeconfig, nil
}

func kubernetesClusterPath(id string) string {
	return "/kubernetes-clusters/" + url.PathEscape(id)
}
