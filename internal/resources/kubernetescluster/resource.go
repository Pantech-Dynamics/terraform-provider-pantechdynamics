// Package kubernetescluster implements the pantechdynamics_kubernetes_cluster
// resource.
package kubernetescluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "k8s_"
	kind     = "Kubernetes cluster"
)

// Default waits. Creating a cluster builds several VMs and bootstraps
// Kubernetes on them, and an upgrade replaces nodes one at a time, so both take
// far longer than the 15 minutes the other resources allow.
const (
	defaultCreateTimeout = 60 * time.Minute
	defaultUpdateTimeout = 60 * time.Minute
	defaultDeleteTimeout = 20 * time.Minute
)

// clusterAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type clusterAPI interface {
	CreateKubernetesCluster(ctx context.Context, req client.CreateKubernetesClusterRequest) (*client.OperationReference, error)
	GetKubernetesCluster(ctx context.Context, id string) (*client.KubernetesCluster, error)
	UpdateKubernetesCluster(ctx context.Context, id string, req client.UpdateKubernetesClusterRequest) (*client.OperationReference, error)
	UpgradeKubernetesCluster(ctx context.Context, id string, req client.UpgradeKubernetesClusterRequest) (*client.OperationReference, error)
	ResizeKubernetesClusterPlan(ctx context.Context, id string, req client.ResizeKubernetesClusterPlanRequest) (*client.OperationReference, error)
	UpgradeKubernetesNetwork(ctx context.Context, id string, req client.UpgradeKubernetesNetworkRequest) (*client.OperationReference, error)
	ConfigureKubernetesEndpointAccess(ctx context.Context, id string, req client.ConfigureKubernetesEndpointAccessRequest) (*client.OperationReference, error)
	DeleteKubernetesCluster(ctx context.Context, id string) (*client.OperationReference, error)
	GetKubeconfig(ctx context.Context, id string) (string, error)
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithModifyPlan     = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one Kubernetes cluster.
type Resource struct {
	api clusterAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_kubernetes_cluster.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	fixedNetwork := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), networkFixedAtCreation()}
	resp.Schema = schema.Schema{
		Description: "A managed Kubernetes cluster. Every node, control and worker, is a VM of node_plan in your account and is billed as one; there is no separate control-plane fee. Storage used by persistent volume claims (volume_storage_gb) is billed with the nodes' disks. workers, autoscaling and api_allowed_cidrs change in place with one PATCH; kubernetes_version_id changes in place to one of available_upgrade_ids (upgrade, never down), and clusters that keep the version they were created with offer none; node_plan is fixed at creation, so changing it replaces the cluster; endpoint_access changes in place. cni_plugin, pod_cidr and service_cidr are fixed when an appliance cluster is created, so changing them replaces it. Set either workers (a fixed count) or autoscaling (a range the cluster autoscaler moves the count within), not both. Changing name, zone_id, subnet_id or control_nodes replaces the cluster. A cluster that does not fit your account's limits fails with PROVISIONING_LIMIT_EXCEEDED before anything is created. Destroying it stops it at once and its nodes, disks and persistent volumes are destroyed 12 hours later. Its subnet stays in use until the nodes are removed in the background, so a pantechdynamics_subnet destroyed in the same run waits for that.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the cluster, starting with k8s_.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"name": schema.StringAttribute{
				Description:   "Name of the cluster: 1 to 40 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen. Unique in the organization. Changing it replaces the cluster.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{nameValidator{}},
			},
			"zone_id": schema.StringAttribute{
				Description:   "Zone to create the cluster in, for example \"af-abj-2\" (a VPC zone, which needs subnet_id) or \"af-abj-1\" (a standard zone, which takes no subnet_id). The pantechdynamics_kubernetes_versions data source lists the versions per zone. Changing it replaces the cluster.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"kubernetes_version_id": schema.StringAttribute{
				Description: "Id of the Kubernetes version, starting with k8sv_, from the pantechdynamics_kubernetes_versions data source in zone_id. Changes in place by upgrading the cluster, which must be running: the new id must be one of available_upgrade_ids (the next patch or the next minor version). Upgrades never go down and never happen on their own. A cluster that keeps the version it was created with has no available_upgrade_ids, and a change is refused with KUBERNETES_UPGRADE_NOT_AVAILABLE: create a new cluster on the version you need.",
				Required:    true,
				Validators:  []validator.String{resourcekit.IDPrefix("Kubernetes version", "k8sv_")},
			},
			"subnet_id": schema.StringAttribute{
				Description:   "Id of the subnet the nodes go in, from pantechdynamics_subnet. Required in a VPC zone, and must be omitted in a standard zone. Changing it replaces the cluster.",
				Optional:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IDPrefix("subnet", "snet_")},
			},
			"node_plan": schema.StringAttribute{
				Description:   "Slug of the plan every node uses, from the pantechdynamics_plans data source. It needs at least the version's min_cpu and min_memory_mb. Each node is billed as a VM of this plan. Fixed when the cluster is created: its nodes keep the size they were built with, so changing it replaces the cluster (a new cluster, new nodes and a new kubeconfig; workloads are not moved). For bigger nodes in a running cluster, add a pantechdynamics_kubernetes_node_pool on that plan instead. The API reports the plan only by id (node_plan_id), so a change made outside Terraform is not detected, and after an import this holds no value and setting it changes nothing.",
				Required:      true,
				PlanModifiers: []planmodifier.String{nodePlanFixedAtCreation()},
			},
			"control_nodes": schema.Int64Attribute{
				Description: "Control plane nodes: 1 (the default), or 3 for a highly available control plane where the zone offers one (see ha_zone_ids on the pantechdynamics_kubernetes_versions data source). Changing it replaces the cluster.",
				Optional:    true,
				Computed:    true,
				// The backend assigns 1 when control_nodes is omitted, and reports
				// the count on read. UseStateForUnknown holds that value across a
				// plan that omits it, and RequiresReplaceIfConfigured replaces the
				// cluster only when a configured value changes, so an import or a
				// removed argument no longer destroys it. A static Default would be
				// re-applied over the read value and force that destroy.
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					int64planmodifier.RequiresReplaceIfConfigured(),
				},
				Validators: []validator.Int64{controlNodesValidator{}},
			},
			"workers": schema.Int64Attribute{
				Description:   "A fixed number of worker nodes, 1 to 10. Required unless autoscaling is enabled, and must not be set while it is: the autoscaler then owns the count, which is reported here and never causes a diff. Changes in place by scaling the cluster, which must be running. Added workers are billed as VMs of node_plan. To go from autoscaling back to a fixed count, remove autoscaling and set workers.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				Validators:    []validator.Int64{resourcekit.IntBetween(minWorkers, maxWorkers)},
			},
			"autoscaling": schema.SingleNestedAttribute{
				Description: "The cluster autoscaler, which adds and removes workers between min_workers and max_workers as pods need them. Workers are billed as VMs of node_plan while they run, and your credit and account limits are checked for max_workers. The cluster starts at min_workers. Changes in place. Removing it (or setting enabled = false) turns the autoscaler off and needs workers set: the count it lands on.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Description: "Whether the autoscaler runs. false is the same as leaving autoscaling out, and then min_workers and max_workers must be omitted.",
						Required:    true,
					},
					"min_workers": schema.Int64Attribute{
						Description: "Fewest workers, 1 to 10. Required when enabled.",
						Optional:    true,
						Validators:  []validator.Int64{resourcekit.IntBetween(minWorkers, maxWorkers)},
					},
					"max_workers": schema.Int64Attribute{
						Description: "Most workers, min_workers to 10. Required when enabled. Credit and account limits are checked for this many.",
						Optional:    true,
						Validators:  []validator.Int64{resourcekit.IntBetween(minWorkers, maxWorkers)},
					},
				},
			},
			"api_allowed_cidrs": schema.SetAttribute{
				Description: "VPC zone only (needs subnet_id): the source addresses allowed to reach the API server, as at most 20 IPv4 CIDRs, for example [\"203.0.113.0/24\"]. Empty (the default) allows any address. Changes in place: the set is the whole list, and removing it allows any address again.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})),
				Validators:  []validator.Set{cidrSetValidator{}},
			},
			"endpoint": schema.StringAttribute{
				Description:   "The API server's URL, once the cluster has one. Null until then. The kubeconfig points kubectl at it.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"cni_plugin": schema.StringAttribute{
				Description:   "The pod network plugin: \"flannel\" (the default), \"wireguard\" (flannel with traffic between nodes encrypted) or \"calico\" (network policies). Fixed when an appliance cluster is created: changing it replaces the cluster.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: fixedNetwork,
				Validators:    []validator.String{resourcekit.OneOf("flannel", "wireguard", "calico")},
			},
			"pod_cidr": schema.StringAttribute{
				Description:   "The pods' IPv4 address range. Left unset, the platform picks the first of 10.42.0.0/16, 10.244.0.0/16, 172.20.0.0/16 and 192.168.0.0/17 that does not overlap the cluster's VPC network, subnet or service_cidr. A range that overlaps them is refused with KUBERNETES_POD_CIDR_OVERLAP. Fixed when an appliance cluster is created: changing it replaces the cluster.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: fixedNetwork,
				Validators:    []validator.String{resourcekit.IPv4CIDR()},
			},
			"service_cidr": schema.StringAttribute{
				Description:   "The services' IPv4 address range. Left unset, the platform picks the first of 10.43.0.0/16, 10.96.0.0/16, 172.21.0.0/16 and 192.168.128.0/17 that does not overlap the cluster's VPC network, subnet or pod_cidr. A range that overlaps them is refused with KUBERNETES_SERVICE_CIDR_OVERLAP. Fixed when an appliance cluster is created: changing it replaces the cluster.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: fixedNetwork,
				Validators:    []validator.String{resourcekit.IPv4CIDR()},
			},
			"endpoint_access": schema.StringAttribute{
				Description:   "Where the API server can be reached from: \"private\" (the default, only from inside the cluster's network), \"public\" (from the internet, limited by api_allowed_cidrs) or \"dual\" (both). public and dual give the API server a public address of its own (public_ip, public_port), billed as a VPC public IP for every hour it is held, and open its port on the subnet to api_allowed_cidrs (or any address). Changes in place; a change to public or dual finishes once the public address is routed.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: keep,
				Validators:    []validator.String{resourcekit.OneOf("public", "private", "dual")},
			},
			"public_ip": schema.StringAttribute{
				Description: "The public address the API server is reached on when endpoint_access is public or dual; null until it has one, and for private.",
				Computed:    true,
			},
			"public_port": schema.Int64Attribute{
				Description: "The public port the API server is reached on when endpoint_access is public or dual; null until it has one, and for private.",
				Computed:    true,
			},
			"kind": schema.StringAttribute{
				Description:   "How the platform runs the cluster, for example \"appliance\".",
				Computed:      true,
				PlanModifiers: keep,
			},
			"volume_storage_gb": schema.Int64Attribute{
				Description: "Storage, in GB, that the workloads' persistent volume claims use. It is billed with the nodes' disks, and changes as workloads create and delete volumes.",
				Computed:    true,
			},
			"kubernetes_version": schema.StringAttribute{
				Description: "The Kubernetes version the cluster runs, for example \"1.31.2\".",
				Computed:    true,
			},
			"network_id": schema.StringAttribute{
				Description:   "Id of the VPC network the nodes are in. Null in a standard zone.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"node_plan_id": schema.StringAttribute{
				Description:   "Id of the plan every node uses.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"node": schema.SingleNestedAttribute{
				Description:   "Size of every node, from node_plan.",
				Computed:      true,
				PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Attributes: map[string]schema.Attribute{
					"vcpu":      schema.Int64Attribute{Computed: true, Description: "Virtual CPUs."},
					"memory_mb": schema.Int64Attribute{Computed: true, Description: "Memory, in MB."},
					"disk_gb":   schema.Int64Attribute{Computed: true, Description: "Disk, in GB."},
				},
			},
			"nodes": schema.Int64Attribute{
				Description: "control_nodes plus workers: the number of VMs billed.",
				Computed:    true,
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the cluster as the platform sees it, for example \"running\", \"updating\" while a scale or upgrade runs, or \"stopped\".",
				Computed:    true,
			},
			"in_sync": schema.BoolAttribute{
				Description: "False while a scale or upgrade has not finished.",
				Computed:    true,
			},
			"node_health": schema.StringAttribute{
				Description: "What Kubernetes last reported of the nodes while the cluster runs: \"healthy\" (every node Ready), \"degraded\" (a node is NotReady, or fewer are Ready than the cluster should run) or \"unknown\" (the nodes could not be read in the last 15 minutes, for example a private endpoint whose agent is not connected). Read about every 5 minutes; null while the cluster is not running. A degraded cluster still reports observed_state \"running\". Always \"degraded\", with node_health_reason, while the cluster's pod_cidr or service_cidr overlaps its own network.",
				Computed:    true,
			},
			"node_health_reason": schema.StringAttribute{
				Description: "Why node_health is \"degraded\" when the cluster itself is the cause rather than a node's last reading: its pod_cidr or service_cidr overlaps the network its nodes are on (possible only for a cluster created before such ranges were refused). Those ranges are fixed at creation, so the fix is a new cluster. Null otherwise.",
				Computed:    true,
			},
			"ready_nodes": schema.Int64Attribute{
				Description: "Nodes Kubernetes reported Ready at nodes_checked_at; null when node_health is unknown or null.",
				Computed:    true,
			},
			"nodes_checked_at": schema.StringAttribute{
				Description: "When the nodes were last read, in RFC 3339 UTC, to the second; null if never.",
				Computed:    true,
			},
			"available_upgrade_ids": schema.ListAttribute{
				Description: "Ids of the Kubernetes versions the cluster can be upgraded to now, the values kubernetes_version_id may change to.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"kube_config": schema.StringAttribute{
				Description:   "The cluster's admin kubeconfig file (YAML), for kubectl. It holds a long-lived cluster-admin credential: treat it, and the Terraform state that stores it, like a password. It is downloaded once the cluster is running (which needs a write API key), and again when the state holds none or the API server's address (endpoint, public_ip or public_port) changed.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the cluster was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"updated_at": schema.StringAttribute{
				Description: "When the cluster was last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(clusterAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ValidateConfig checks, at plan time, the rules that tie workers, autoscaling,
// subnet_id and api_allowed_cidrs together.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	validateConfig(ctx, req.Config, &resp.Diagnostics)
}

// ModifyPlan refuses, at plan time, a version change that is not an available
// upgrade, and leaves workers unknown when the autoscaler may move it during
// the change.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	checkUpgrade(ctx, req, resp)
	planWorkers(ctx, req, resp)
	planNodeResize(ctx, req, resp)
}

// Create orders the cluster, saves its id straight away, waits until it is
// running, then downloads its kubeconfig.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Create, defaultCreateTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	body, diags := toCreateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref, err := r.api.CreateKubernetesCluster(ctx, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error creating Kubernetes cluster", err, createHints)
		return
	}
	id := ref.ResourceID
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, id))...)

	if err := r.wait(ctx, ref.OperationID, id); err != nil {
		addWaitError(&resp.Diagnostics, "Error creating Kubernetes cluster", id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// Read refreshes state. A cluster deleted outside Terraform is removed from
// state. The kubeconfig is downloaded only when the state holds none or the
// API server's address changed, so a refresh does not mint a credential each
// time.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	k, err := r.api.GetKubernetesCluster(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && isGone(k)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading Kubernetes cluster", err, nil)
		return
	}
	m, diags := fromAPIResponse(ctx, state, k)
	resp.Diagnostics.Append(diags...)
	r.fillKubeConfig(ctx, state, &m, k, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Update upgrades the cluster when kubernetes_version_id changed, moves its
// nodes when node_plan changed, rebuilds the pod network when cni_plugin,
// pod_cidr or service_cidr changed, sends one PATCH with whichever of workers,
// autoscaling and api_allowed_cidrs changed, then sets endpoint_access,
// waiting for each to finish. A plan that changes only the timeouts
// block, or fills node_plan after an import, sends nothing.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Update, defaultUpdateTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	prev := state
	prev.NodePlan, prev.Timeouts = plan.NodePlan, plan.Timeouts // only Terraform holds these

	if !plan.KubernetesVersionID.Equal(state.KubernetesVersionID) {
		ref, err := r.api.UpgradeKubernetesCluster(ctx, id, client.UpgradeKubernetesClusterRequest{KubernetesVersionID: plan.KubernetesVersionID.ValueString()})
		if !r.applied(ctx, ref, err, "Error upgrading Kubernetes cluster", prev, id, &resp.State, &resp.Diagnostics) {
			return
		}
	}
	if slug, ok := nodePlanChange(state.NodePlan, plan.NodePlan); ok {
		ref, err := r.api.ResizeKubernetesClusterPlan(ctx, id, client.ResizeKubernetesClusterPlanRequest{NodePlan: slug})
		if !r.applied(ctx, ref, err, "Error resizing the nodes of Kubernetes cluster", state, id, &resp.State, &resp.Diagnostics) {
			return
		}
	}
	if net, ok := toNetworkRequest(state, plan); ok {
		ref, err := r.api.UpgradeKubernetesNetwork(ctx, id, net)
		if !r.applied(ctx, ref, err, "Error changing the network of Kubernetes cluster", prev, id, &resp.State, &resp.Diagnostics) {
			return
		}
	}
	body, changed, diags := toUpdateRequest(ctx, state, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if changed {
		ref, err := r.api.UpdateKubernetesCluster(ctx, id, body)
		if !r.applied(ctx, ref, err, "Error changing Kubernetes cluster", prev, id, &resp.State, &resp.Diagnostics) {
			return
		}
	}
	if mode, ok := endpointAccessChange(state.EndpointAccess, plan.EndpointAccess); ok {
		ref, err := r.api.ConfigureKubernetesEndpointAccess(ctx, id, client.ConfigureKubernetesEndpointAccessRequest{EndpointAccess: mode})
		if !r.applied(ctx, ref, err, "Error changing the endpoint access of Kubernetes cluster", prev, id, &resp.State, &resp.Diagnostics) {
			return
		}
	}
	shaped := prev
	shaped.Autoscaling = plan.Autoscaling // an autoscaler that is off is stored as the configuration wrote it
	r.refresh(ctx, shaped, id, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, prev)...) // the next refresh catches up
	}
}

// Delete deletes the cluster and waits until the platform has taken it out of
// service. The platform stops and hides it at once and destroys its nodes and
// disks 12 hours later; that is not waited for. One already gone counts as
// success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, state.Timeouts.Delete, defaultDeleteTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	ref, err := r.api.DeleteKubernetesCluster(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error deleting Kubernetes cluster", err, updateHints)
		return
	}
	done := r.goneCheck(id)
	if ref.OperationID != "" {
		err = r.api.WaitForOperation(ctx, ref.OperationID, done)
	} else { // already being deleted
		err = r.api.WaitUntil(ctx, kind+" "+id+" to be deleted", done)
	}
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting Kubernetes cluster", kind, id, err)
	}
}

// ImportState adopts an existing cluster by id. node_plan cannot be read back
// and stays empty until the configuration sets it.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// applied reports a scale or upgrade request and its wait. On a refusal or a
// failed wait it stores what the platform reports, or the previous state, and
// returns false.
func (r *Resource) applied(ctx context.Context, ref *client.OperationReference, err error, summary string, prev model, id string, st *tfsdk.State, diags *diag.Diagnostics) bool {
	if err != nil {
		addAPIError(diags, summary, err, updateHints)
		diags.Append(st.Set(ctx, prev)...) // nothing changed
		return false
	}
	if err := r.wait(ctx, ref.OperationID, id); err != nil {
		addWaitError(diags, summary, id, err)
		r.refreshOrKeep(ctx, prev, id, st, diags)
		return false
	}
	return true
}

// wait follows an operation, if there is one, and then the cluster itself
// until it is running and in sync. The second wait matters because the
// operation record can report success before the cluster has settled.
func (r *Resource) wait(ctx context.Context, opID, id string) error {
	done := r.settledCheck(id, opID)
	if opID != "" {
		if err := r.api.WaitForOperation(ctx, opID, done); err != nil {
			return err
		}
	}
	return r.api.WaitUntil(ctx, kind+" "+id+" to be running", done)
}

// refresh reads the cluster and stores it, keeping what only Terraform knows
// from prev and downloading the kubeconfig if prev holds none or the API
// server moved.
func (r *Resource) refresh(ctx context.Context, prev model, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	k, err := r.api.GetKubernetesCluster(ctx, id)
	if err != nil {
		resourcekit.AddAPIError(diags, "Error reading Kubernetes cluster after the change", err, nil)
		return
	}
	m, d := fromAPIResponse(ctx, prev, k)
	diags.Append(d...)
	r.fillKubeConfig(ctx, prev, &m, k, diags)
	diags.Append(st.Set(ctx, m)...)
}

// refreshOrKeep stores what the platform reports after a failed change, or the
// previous state when it cannot be read, so state never holds planned values
// that were not applied.
func (r *Resource) refreshOrKeep(ctx context.Context, prev model, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	k, err := r.api.GetKubernetesCluster(ctx, id)
	if err != nil {
		diags.Append(st.Set(ctx, prev)...)
		return
	}
	m, d := fromAPIResponse(ctx, prev, k)
	diags.Append(d...)
	diags.Append(st.Set(ctx, m)...)
}

// fillKubeConfig downloads the kubeconfig into m when the cluster is running
// and m holds none, or the API server's address differs from prev's, since
// the old file then points kubectl at the wrong place. A failure is a
// warning, not an error: the cluster is fine, and the next refresh tries
// again. After a failed download for a moved address, m keeps prev's address
// so that the next refresh still sees the move.
func (r *Resource) fillKubeConfig(ctx context.Context, prev model, m *model, k *client.KubernetesCluster, diags *diag.Diagnostics) {
	if k.ObservedState != client.KubernetesRunning {
		return
	}
	moved := hasKubeConfig(*m) && apiAddressChanged(prev, *m)
	if hasKubeConfig(*m) && !moved {
		return
	}
	kc, err := r.api.GetKubeconfig(ctx, k.ID)
	if err != nil {
		detail := "The kubeconfig of Kubernetes cluster " + k.ID + " could not be downloaded, so kube_config is empty. The next refresh tries again."
		if moved {
			detail = "The API server of Kubernetes cluster " + k.ID + " moved, but its new kubeconfig could not be downloaded, so kube_config still holds the old one. The next refresh tries again."
			m.Endpoint, m.PublicIP, m.PublicPort = prev.Endpoint, prev.PublicIP, prev.PublicPort
		}
		diags.AddWarning("Kubeconfig not downloaded", detail+" Downloading it needs an API key with the write scope.\n\n"+err.Error())
		return
	}
	m.KubeConfig = types.StringValue(kc)
}

// settledCheck finishes a wait once the cluster is running, in sync and meant
// to run. A failed cluster ends the wait with the operation's failure code and
// reason when there is one, or the cluster's own.
func (r *Resource) settledCheck(id, opID string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		k, err := r.api.GetKubernetesCluster(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		if k.ObservedState == client.KubernetesFailed {
			return false, r.failedError(ctx, k, opID)
		}
		return k.ObservedState == client.KubernetesRunning && k.InSync && k.DesiredState == client.KubernetesRunning && publiclyRouted(k), nil
	}
}

// publiclyRouted reports whether a public or dual cluster in a VPC zone has
// its public address yet; a private cluster, or one in a standard zone, needs
// none. Without it the apply would finish with public_ip null and a
// kubeconfig that kubectl cannot use from outside.
func publiclyRouted(k *client.KubernetesCluster) bool {
	if k.SubnetID == nil || k.EndpointAccess == "" || k.EndpointAccess == "private" {
		return true
	}
	return k.PublicIP != nil && k.PublicPort != nil
}

// goneCheck finishes a delete once the cluster is gone or out of service.
func (r *Resource) goneCheck(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		k, err := r.api.GetKubernetesCluster(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return isGone(k), nil
	}
}

// isGone reports whether a cluster no longer counts as existing: deleted or
// retired, or meant to be deleted and already stopped or failed. The platform
// keeps a deleted cluster's nodes for 12 hours, so "stopped" with a deleted
// intent is as far as a delete gets at first.
func isGone(k *client.KubernetesCluster) bool {
	switch k.ObservedState {
	case client.KubernetesDeleted, client.KubernetesRetired:
		return true
	case client.KubernetesStopped, client.KubernetesFailed:
		return k.DesiredState == client.KubernetesDeleted
	}
	return false
}

// failedError prefers the operation's failure, which carries the code the
// hints know, then the cluster's own failure.
func (r *Resource) failedError(ctx context.Context, k *client.KubernetesCluster, opID string) error {
	if opID != "" {
		if op, err := r.api.GetOperation(ctx, opID); err == nil && op.Status == client.OperationFailed {
			return &client.OperationError{Operation: *op}
		}
	}
	msg := fmt.Sprintf("%s %s entered the %q state", kind, k.ID, client.KubernetesFailed)
	if k.FailureCode != nil {
		msg += ": " + *k.FailureCode
	}
	if k.FailureReason != nil {
		msg += ": " + *k.FailureReason
	}
	return errors.New(msg)
}

// addWaitError reports a failed wait. A cluster that does not fit the
// account's limits gets a hint naming what counts against them.
func addWaitError(diags *diag.Diagnostics, summary, id string, err error) {
	var opErr *client.OperationError
	if errors.As(err, &opErr) && opErr.Operation.Failure != nil && opErr.Operation.Failure.Code == resourcekit.FailureLimitExceeded {
		diags.AddError(summary, resourcekit.WithFailureHint(err)+"\n\n"+hintLimitExceeded)
		return
	}
	resourcekit.AddWaitError(diags, summary, kind, id, err)
}

// withTimeout applies the configured timeout, or def, to ctx.
func withTimeout(ctx context.Context, get resourcekit.Timeout, def time.Duration, diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, def)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}

// addAPIError reports a refused request. Field errors from a 422 point at
// their attributes, with a hint for the field codes a user can act on; any
// other error gets the hint for its problem code.
func addAPIError(diags *diag.Diagnostics, summary string, err error, hints map[string]string) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && len(apiErr.Errors) > 0 && allMapped(apiErr.Errors) {
		for _, fe := range apiErr.Errors {
			p, _ := attributeFor(fe.Field)
			detail := apiErr.FieldDetail(fe)
			if hint := fieldHints[fe.Code]; hint != "" {
				detail += "\n\n" + hint
			}
			diags.AddAttributeError(p, summary, detail)
		}
		return
	}
	resourcekit.AddHintedError(diags, summary, err, hints)
}

func allMapped(fields []client.FieldError) bool {
	for _, fe := range fields {
		if _, ok := attributeFor(fe.Field); !ok {
			return false
		}
	}
	return true
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "zone_id", "kubernetes_version_id", "subnet_id", "node_plan", "control_nodes", "workers", "autoscaling", "api_allowed_cidrs",
		"cni_plugin", "pod_cidr", "service_cidr", "endpoint_access":
		return path.Root(field), true
	}
	return path.Path{}, false
}

const (
	hintUnavailable   = "Managed Kubernetes is not open on this platform yet. Contact support to ask when it will be."
	hintLimitExceeded = "Every node is a VM in your account and counts against its limits: control_nodes plus workers, or plus max_workers with autoscaling. The platform checks this before it creates or adds anything, so nothing was left behind by the refusal. Lower workers or max_workers, delete VMs or clusters you no longer use, or contact support to raise the limits."
)

// createHints explain the create refusals a user can act on.
var createHints = map[string]string{
	client.CodeKubernetesUnavailable:      hintUnavailable,
	client.CodeKubernetesClusterNameTaken: "You already have a cluster with this name. Choose another name, or bring the existing cluster under Terraform with `terraform import`.",
	client.CodeKubernetesClusterLimit:     "Your organization has reached its Kubernetes cluster limit. Delete a cluster you no longer use, or contact support to raise the limit.",
	client.CodeInsufficientCredit:         "Your credit does not cover the first payment for every node (control_nodes plus workers, or plus max_workers with autoscaling, each billed as a VM of node_plan). Top up or add a card, or lower workers or max_workers, then run `terraform apply` again.",
}

// updateHints explain the scale, upgrade and delete refusals a user can act on.
var updateHints = map[string]string{
	client.CodeKubernetesUnavailable:           hintUnavailable,
	client.CodeKubernetesClusterNotRunning:     "Scaling, autoscaling, allow-list changes and upgrades need a running cluster. Start it from the console or the API, then run `terraform apply` again.",
	client.CodeKubernetesClusterBusy:           "The cluster is already changing. Wait until it is running again, then run `terraform apply` again.",
	client.CodeKubernetesClusterNotChangeable:  "The cluster is stopped, being deleted, or failed, so it cannot be changed. Start a stopped cluster first, or replace a failed one with `terraform apply -replace`.",
	client.CodeKubernetesNetworkNotChangeable:  "This cluster's cni_plugin, pod_cidr and service_cidr were fixed when it was created. Set them back to the values in state, or replace the cluster with `terraform apply -replace`.",
	client.CodeKubernetesNodePlanNotChangeable: "This cluster's node_plan was fixed when it was created; its nodes keep their size. Set node_plan back to the value in state, add a pantechdynamics_kubernetes_node_pool on the plan you need, or replace the cluster with `terraform apply -replace`.",
	client.CodeKubernetesUpgradeNotAvailable:   "This cluster runs the Kubernetes version it was created with and cannot be upgraded in place (available_upgrade_ids is empty). Set kubernetes_version_id back to the value in state, or create a new cluster on the version you need and move your workloads to it.",
	client.CodeInsufficientCredit:              "Your credit does not cover the added workers (with autoscaling, a max_workers above today's count is checked as if every worker ran). Top up or add a card, then run `terraform apply` again.",
}

// fieldHints explain the 422 field codes a user can act on.
var fieldHints = map[string]string{
	client.CodeInvalidKubernetesZone:        "Use a zone your organization can use; see the pantechdynamics_regions data source.",
	client.CodeInvalidKubernetesVersion:     "Use an id from the pantechdynamics_kubernetes_versions data source, filtered to zone_id.",
	client.CodeInvalidKubernetesNodePlan:    "Choose an active plan with at least the version's min_cpu and min_memory_mb (see pantechdynamics_kubernetes_versions and pantechdynamics_plans).",
	client.CodeKubernetesHANotAvailable:     "This zone does not offer a highly available control plane now. Set control_nodes = 1, or use a zone in ha_zone_ids of the pantechdynamics_kubernetes_versions data source.",
	client.CodeKubernetesSubnetRequired:     "zone_id is a VPC zone: set subnet_id to an active pantechdynamics_subnet in it.",
	client.CodeKubernetesSubnetNotAllowed:   "zone_id is a standard zone: remove subnet_id.",
	client.CodeKubernetesSubnetNotFound:     "subnet_id must be an active subnet in zone_id.",
	client.CodeKubernetesUpgradeNotAllowed:  "Set kubernetes_version_id to one of available_upgrade_ids: upgrades go one minor version at a time and never down.",
	client.CodeInvalidKubernetesAutoscaling: "Set min_workers and max_workers between 1 and 10, with min_workers no more than max_workers, and leave workers out while autoscaling is enabled: the autoscaler sets it.",
	client.CodeInvalidAPIAllowedCIDRs:       "Use at most 20 IPv4 CIDRs with their host bits clear, for example \"203.0.113.0/24\".",
	client.CodeAPIAllowedCIDRsVPCOnly:       "The API server allow-list is offered only for clusters in a VPC zone (with subnet_id). Remove api_allowed_cidrs for a cluster in a standard zone.",
	client.CodeKubernetesPodCIDROverlap:     "pod_cidr overlaps the cluster's VPC network, its subnet or service_cidr, so pods could not reach the nodes. Choose a range clear of all three, or leave pod_cidr unset and the platform picks one.",
	client.CodeKubernetesServiceCIDROverlap: "service_cidr overlaps the cluster's VPC network, its subnet or pod_cidr. Choose a range clear of all three, or leave service_cidr unset and the platform picks one.",
}
