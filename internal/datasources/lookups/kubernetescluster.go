package lookups

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// kubernetesClusterAPI is the part of the API client the lookup needs.
type kubernetesClusterAPI interface {
	ListKubernetesClusters(ctx context.Context) ([]client.KubernetesCluster, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &KubernetesClusterDataSource{}
	_ datasource.DataSourceWithValidateConfig = &KubernetesClusterDataSource{}
)

// KubernetesClusterDataSource looks up one Kubernetes cluster by id or name.
// It exposes metadata only: the kubeconfig is a cluster-admin credential and is
// left to the resource, so a read-only lookup never stores one.
type KubernetesClusterDataSource struct {
	api kubernetesClusterAPI
}

// NewKubernetesCluster is the factory the provider registers.
func NewKubernetesCluster() datasource.DataSource { return &KubernetesClusterDataSource{} }

type kubernetesClusterModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	ZoneID              types.String `tfsdk:"zone_id"`
	KubernetesVersionID types.String `tfsdk:"kubernetes_version_id"`
	KubernetesVersion   types.String `tfsdk:"kubernetes_version"`
	NetworkID           types.String `tfsdk:"network_id"`
	SubnetID            types.String `tfsdk:"subnet_id"`
	NodePlanID          types.String `tfsdk:"node_plan_id"`
	Node                types.Object `tfsdk:"node"`
	ControlNodes        types.Int64  `tfsdk:"control_nodes"`
	Workers             types.Int64  `tfsdk:"workers"`
	Nodes               types.Int64  `tfsdk:"nodes"`
	ObservedState       types.String `tfsdk:"observed_state"`
	InSync              types.Bool   `tfsdk:"in_sync"`
	AvailableUpgradeIDs types.List   `tfsdk:"available_upgrade_ids"`
	Autoscaling         types.Object `tfsdk:"autoscaling"`
	APIAllowedCIDRs     types.List   `tfsdk:"api_allowed_cidrs"`
	Endpoint            types.String `tfsdk:"endpoint"`
	VolumeStorageGB     types.Int64  `tfsdk:"volume_storage_gb"`
	CreatedAt           types.String `tfsdk:"created_at"`
}

var kubernetesNodeAttrTypes = map[string]attr.Type{
	"vcpu":      types.Int64Type,
	"memory_mb": types.Int64Type,
	"disk_gb":   types.Int64Type,
}

var kubernetesAutoscalingAttrTypes = map[string]attr.Type{
	"enabled":     types.BoolType,
	"min_workers": types.Int64Type,
	"max_workers": types.Int64Type,
}

// Metadata sets the type name: pantechdynamics_kubernetes_cluster.
func (d *KubernetesClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

// Schema describes the attributes.
func (d *KubernetesClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := selectorAttributes("Kubernetes cluster", "k8s_", true)
	attrs["zone_id"] = schema.StringAttribute{Computed: true, Description: "Zone the cluster is in."}
	attrs["kubernetes_version_id"] = schema.StringAttribute{Computed: true, Description: "Id of the Kubernetes version the cluster runs."}
	attrs["kubernetes_version"] = schema.StringAttribute{Computed: true, Description: "The Kubernetes version the cluster runs, for example \"1.31.2\"."}
	attrs["network_id"] = schema.StringAttribute{Computed: true, Description: "Id of the VPC network the nodes are in. Null in a standard zone."}
	attrs["subnet_id"] = schema.StringAttribute{Computed: true, Description: "Id of the subnet the nodes are in. Null in a standard zone."}
	attrs["node_plan_id"] = schema.StringAttribute{Computed: true, Description: "Id of the plan every node uses."}
	attrs["node"] = schema.SingleNestedAttribute{
		Computed:    true,
		Description: "Size of every node.",
		Attributes: map[string]schema.Attribute{
			"vcpu":      schema.Int64Attribute{Computed: true, Description: "Virtual CPUs."},
			"memory_mb": schema.Int64Attribute{Computed: true, Description: "Memory, in MB."},
			"disk_gb":   schema.Int64Attribute{Computed: true, Description: "Disk, in GB."},
		},
	}
	attrs["control_nodes"] = schema.Int64Attribute{Computed: true, Description: "Control plane nodes: 1, or 3 for a highly available control plane."}
	attrs["workers"] = schema.Int64Attribute{Computed: true, Description: "Worker nodes."}
	attrs["nodes"] = schema.Int64Attribute{Computed: true, Description: "control_nodes plus workers: the number of VMs billed."}
	attrs["observed_state"] = schema.StringAttribute{Computed: true, Description: "State of the cluster as the platform sees it, for example \"running\"."}
	attrs["in_sync"] = schema.BoolAttribute{Computed: true, Description: "False while a scale or upgrade has not finished."}
	attrs["available_upgrade_ids"] = schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Ids of the Kubernetes versions the cluster can be upgraded to now."}
	attrs["autoscaling"] = schema.SingleNestedAttribute{
		Computed:    true,
		Description: "The cluster autoscaler. When enabled it moves workers between min_workers and max_workers; when not, both bounds are 0.",
		Attributes: map[string]schema.Attribute{
			"enabled":     schema.BoolAttribute{Computed: true, Description: "Whether the autoscaler runs."},
			"min_workers": schema.Int64Attribute{Computed: true, Description: "Fewest workers, or 0 when autoscaling is off."},
			"max_workers": schema.Int64Attribute{Computed: true, Description: "Most workers, or 0 when autoscaling is off."},
		},
	}
	attrs["api_allowed_cidrs"] = schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "IPv4 CIDRs allowed to reach the API server, sorted. Empty means any address."}
	attrs["endpoint"] = schema.StringAttribute{Computed: true, Description: "The API server's URL. Null until the cluster has one."}
	attrs["volume_storage_gb"] = schema.Int64Attribute{Computed: true, Description: "Storage, in GB, that the workloads' persistent volume claims use, billed with the nodes' disks."}
	attrs["created_at"] = schema.StringAttribute{Computed: true, Description: "When the cluster was created, in RFC 3339 UTC, to the second."}
	resp.Schema = schema.Schema{
		Description: "Looks up one existing Kubernetes cluster by id or name, to read its version, network, size, autoscaling range, API server endpoint and allow-list, or volume storage without managing it. It does not expose the kubeconfig: manage the cluster with pantechdynamics_kubernetes_cluster, or download the kubeconfig from the console or the API. Failed and deleted clusters are not found.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *KubernetesClusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configureAPI[kubernetesClusterAPI](req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *KubernetesClusterDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	validateSelector(ctx, req, "Kubernetes cluster", &resp.Diagnostics)
}

// Read lists the clusters and picks the one asked for.
func (d *KubernetesClusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesClusterModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	clusters, err := d.api.ListKubernetesClusters(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes clusters", err.Error())
		return
	}
	id, name := selector(cfg.ID, cfg.Name)
	k, err := lookup.Pick(clusters, "Kubernetes cluster", id, name,
		func(c client.KubernetesCluster) string { return c.ID }, func(c client.KubernetesCluster) string { return c.Name })
	if err != nil {
		resp.Diagnostics.AddError("Kubernetes cluster not found", err.Error())
		return
	}
	node, diags := types.ObjectValue(kubernetesNodeAttrTypes, map[string]attr.Value{
		"vcpu": types.Int64Value(k.Node.VCPU), "memory_mb": types.Int64Value(k.Node.MemoryMB), "disk_gb": types.Int64Value(k.Node.DiskGB),
	})
	resp.Diagnostics.Append(diags...)
	upgrades := make([]string, 0, len(k.AvailableUpgrades))
	for _, v := range k.AvailableUpgrades {
		upgrades = append(upgrades, v.ID)
	}
	upgradeList, diags := types.ListValueFrom(ctx, types.StringType, upgrades)
	resp.Diagnostics.Append(diags...)
	auto, diags := types.ObjectValue(kubernetesAutoscalingAttrTypes, map[string]attr.Value{
		"enabled":     types.BoolValue(k.Autoscaling.Enabled),
		"min_workers": types.Int64Value(k.Autoscaling.MinWorkers),
		"max_workers": types.Int64Value(k.Autoscaling.MaxWorkers),
	})
	resp.Diagnostics.Append(diags...)
	cidrs := k.APIAllowedCIDRs
	if cidrs == nil {
		cidrs = []string{}
	}
	cidrList, diags := types.ListValueFrom(ctx, types.StringType, cidrs)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, kubernetesClusterModel{
		ID: types.StringValue(k.ID), Name: types.StringValue(k.Name), ZoneID: types.StringValue(k.ZoneID),
		KubernetesVersionID: types.StringValue(k.KubernetesVersionID), KubernetesVersion: types.StringValue(k.KubernetesVersion),
		NetworkID: resourcekit.OptionalString(k.NetworkID), SubnetID: resourcekit.OptionalString(k.SubnetID),
		NodePlanID: types.StringValue(k.NodePlanID), Node: node,
		ControlNodes: types.Int64Value(k.ControlNodes), Workers: types.Int64Value(k.Workers), Nodes: types.Int64Value(k.Nodes),
		ObservedState: types.StringValue(k.ObservedState), InSync: types.BoolValue(k.InSync),
		AvailableUpgradeIDs: upgradeList, Autoscaling: auto, APIAllowedCIDRs: cidrList,
		Endpoint: resourcekit.OptionalString(k.Endpoint), VolumeStorageGB: types.Int64Value(k.VolumeStorageGB),
		CreatedAt: resourcekit.Timestamp(k.CreatedAt),
	})...)
}
