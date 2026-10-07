package kubernetescluster

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go.
type model struct {
	ID                  types.String   `tfsdk:"id"`
	Name                types.String   `tfsdk:"name"`
	ZoneID              types.String   `tfsdk:"zone_id"`
	KubernetesVersionID types.String   `tfsdk:"kubernetes_version_id"`
	SubnetID            types.String   `tfsdk:"subnet_id"`
	NodePlan            types.String   `tfsdk:"node_plan"`
	ControlNodes        types.Int64    `tfsdk:"control_nodes"`
	Workers             types.Int64    `tfsdk:"workers"`
	KubernetesVersion   types.String   `tfsdk:"kubernetes_version"`
	NetworkID           types.String   `tfsdk:"network_id"`
	NodePlanID          types.String   `tfsdk:"node_plan_id"`
	Node                types.Object   `tfsdk:"node"`
	Nodes               types.Int64    `tfsdk:"nodes"`
	ObservedState       types.String   `tfsdk:"observed_state"`
	InSync              types.Bool     `tfsdk:"in_sync"`
	AvailableUpgradeIDs types.List     `tfsdk:"available_upgrade_ids"`
	Autoscaling         types.Object   `tfsdk:"autoscaling"`
	APIAllowedCIDRs     types.Set      `tfsdk:"api_allowed_cidrs"`
	Endpoint            types.String   `tfsdk:"endpoint"`
	VolumeStorageGB     types.Int64    `tfsdk:"volume_storage_gb"`
	KubeConfig          types.String   `tfsdk:"kube_config"`
	CreatedAt           types.String   `tfsdk:"created_at"`
	UpdatedAt           types.String   `tfsdk:"updated_at"`
	Timeouts            timeouts.Value `tfsdk:"timeouts"`
}

// nodeAttrTypes is the shape of the node object.
var nodeAttrTypes = map[string]attr.Type{
	"vcpu":      types.Int64Type,
	"memory_mb": types.Int64Type,
	"disk_gb":   types.Int64Type,
}

// autoscalingAttrTypes is the shape of the autoscaling object.
var autoscalingAttrTypes = map[string]attr.Type{
	"enabled":     types.BoolType,
	"min_workers": types.Int64Type,
	"max_workers": types.Int64Type,
}

// autoscalingModel maps the autoscaling object to Go. min_workers and
// max_workers are null when autoscaling is off.
type autoscalingModel struct {
	Enabled    types.Bool  `tfsdk:"enabled"`
	MinWorkers types.Int64 `tfsdk:"min_workers"`
	MaxWorkers types.Int64 `tfsdk:"max_workers"`
}

// toCreateRequest builds the create body. An unset subnet_id is omitted, as a
// standard zone requires. With autoscaling enabled, workers is unknown at
// create (the autoscaler owns it), so it is left out and the cluster starts at
// min_workers. An empty api_allowed_cidrs is left out: any address.
func toCreateRequest(ctx context.Context, plan model) (client.CreateKubernetesClusterRequest, diag.Diagnostics) {
	cidrs, diags := stringsOf(ctx, plan.APIAllowedCIDRs)
	if len(cidrs) == 0 {
		cidrs = nil // left out: any address
	}
	auto, d := autoscalingOf(ctx, plan.Autoscaling)
	diags.Append(d...)
	req := client.CreateKubernetesClusterRequest{
		Name:                plan.Name.ValueString(),
		ZoneID:              plan.ZoneID.ValueString(),
		KubernetesVersionID: plan.KubernetesVersionID.ValueString(),
		SubnetID:            plan.SubnetID.ValueString(),
		NodePlan:            plan.NodePlan.ValueString(),
		ControlNodes:        plan.ControlNodes.ValueInt64(),
		Workers:             plan.Workers.ValueInt64(), // 0, so omitted, when unknown
		APIAllowedCIDRs:     cidrs,
	}
	if auto.Enabled {
		req.Autoscaling = &auto
	}
	return req, diags
}

// toUpdateRequest builds the PATCH body from what differs between state and
// plan. changed is false when nothing the PATCH carries differs, for example
// when only the version or the timeouts block changed. workers is sent only
// when autoscaling will be off, because the backend refuses a count while the
// autoscaler owns it; turning autoscaling off and setting workers in one
// request is accepted.
func toUpdateRequest(ctx context.Context, state, plan model) (req client.UpdateKubernetesClusterRequest, changed bool, diags diag.Diagnostics) {
	planAuto, d := autoscalingOf(ctx, plan.Autoscaling)
	diags.Append(d...)
	stateAuto, d := autoscalingOf(ctx, state.Autoscaling)
	diags.Append(d...)
	if planAuto != stateAuto {
		req.Autoscaling, changed = &planAuto, true
	}
	if !planAuto.Enabled && !plan.Workers.IsUnknown() && !plan.Workers.IsNull() && !plan.Workers.Equal(state.Workers) {
		workers := plan.Workers.ValueInt64()
		req.Workers, changed = &workers, true
	}
	if !plan.APIAllowedCIDRs.Equal(state.APIAllowedCIDRs) {
		cidrs, d := stringsOf(ctx, plan.APIAllowedCIDRs)
		diags.Append(d...)
		if cidrs == nil {
			cidrs = []string{} // the whole new list is empty: allow any address
		}
		req.APIAllowedCIDRs, changed = &cidrs, true
	}
	return req, changed, diags
}

// autoscalingOf reads the autoscaling object as the API shape. Null (or
// enabled = false) is autoscaling off, which the API spells with zero bounds.
func autoscalingOf(ctx context.Context, obj types.Object) (client.KubernetesAutoscaling, diag.Diagnostics) {
	if obj.IsNull() || obj.IsUnknown() {
		return client.KubernetesAutoscaling{}, nil
	}
	var m autoscalingModel
	diags := obj.As(ctx, &m, basetypes.ObjectAsOptions{})
	if diags.HasError() || !m.Enabled.ValueBool() {
		return client.KubernetesAutoscaling{}, diags
	}
	return client.KubernetesAutoscaling{Enabled: true, MinWorkers: m.MinWorkers.ValueInt64(), MaxWorkers: m.MaxWorkers.ValueInt64()}, diags
}

// autoscalingValue builds the state's autoscaling object from the API. When
// autoscaling is off the API reports zero bounds, which no configuration
// writes, so the object follows prev: null when prev held none (or had
// autoscaling on, which shows the change made outside Terraform), and
// {enabled = false} when the configuration says so explicitly.
func autoscalingValue(ctx context.Context, prev types.Object, a client.KubernetesAutoscaling) (types.Object, diag.Diagnostics) {
	if a.Enabled {
		return types.ObjectValue(autoscalingAttrTypes, map[string]attr.Value{
			"enabled":     types.BoolValue(true),
			"min_workers": types.Int64Value(a.MinWorkers),
			"max_workers": types.Int64Value(a.MaxWorkers),
		})
	}
	if prev.IsNull() || prev.IsUnknown() {
		return types.ObjectNull(autoscalingAttrTypes), nil
	}
	var m autoscalingModel
	if diags := prev.As(ctx, &m, basetypes.ObjectAsOptions{}); diags.HasError() || m.Enabled.ValueBool() {
		return types.ObjectNull(autoscalingAttrTypes), diags
	}
	return types.ObjectValue(autoscalingAttrTypes, map[string]attr.Value{
		"enabled":     types.BoolValue(false),
		"min_workers": types.Int64Null(),
		"max_workers": types.Int64Null(),
	})
}

// autoscalingEnabled reports whether the object turns autoscaling on. Unknown
// counts as off.
func autoscalingEnabled(ctx context.Context, obj types.Object) bool {
	a, _ := autoscalingOf(ctx, obj)
	return a.Enabled
}

// stringsOf reads a set of strings; null or unknown is nil.
func stringsOf(ctx context.Context, set types.Set) ([]string, diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := set.ElementsAs(ctx, &out, false)
	return out, diags
}

// fromAPIResponse builds the state from the API object. prev supplies what the
// API does not return: node_plan (it reports node_plan_id), the kubeconfig
// (fetched separately) and the timeouts block.
func fromAPIResponse(ctx context.Context, prev model, k *client.KubernetesCluster) (model, diag.Diagnostics) {
	node, diags := types.ObjectValue(nodeAttrTypes, map[string]attr.Value{
		"vcpu":      types.Int64Value(k.Node.VCPU),
		"memory_mb": types.Int64Value(k.Node.MemoryMB),
		"disk_gb":   types.Int64Value(k.Node.DiskGB),
	})
	upgrades, d := types.ListValueFrom(ctx, types.StringType, upgradeIDs(k))
	diags.Append(d...)
	auto, d := autoscalingValue(ctx, prev.Autoscaling, k.Autoscaling)
	diags.Append(d...)
	cidrs := k.APIAllowedCIDRs
	if cidrs == nil {
		cidrs = []string{}
	}
	cidrSet, d := types.SetValueFrom(ctx, types.StringType, cidrs)
	diags.Append(d...)
	return model{
		ID:                  types.StringValue(k.ID),
		Name:                types.StringValue(k.Name),
		ZoneID:              types.StringValue(k.ZoneID),
		KubernetesVersionID: types.StringValue(k.KubernetesVersionID),
		SubnetID:            resourcekit.OptionalString(k.SubnetID),
		NodePlan:            nullIfUnknown(prev.NodePlan),
		ControlNodes:        types.Int64Value(k.ControlNodes),
		Workers:             types.Int64Value(k.Workers),
		KubernetesVersion:   types.StringValue(k.KubernetesVersion),
		NetworkID:           resourcekit.OptionalString(k.NetworkID),
		NodePlanID:          types.StringValue(k.NodePlanID),
		Node:                node,
		Nodes:               types.Int64Value(k.Nodes),
		ObservedState:       types.StringValue(k.ObservedState),
		InSync:              types.BoolValue(k.InSync),
		AvailableUpgradeIDs: upgrades,
		Autoscaling:         auto,
		APIAllowedCIDRs:     cidrSet,
		Endpoint:            resourcekit.OptionalString(k.Endpoint),
		VolumeStorageGB:     types.Int64Value(k.VolumeStorageGB),
		KubeConfig:          nullIfUnknown(prev.KubeConfig),
		CreatedAt:           resourcekit.Timestamp(k.CreatedAt),
		UpdatedAt:           resourcekit.Timestamp(k.UpdatedAt),
		Timeouts:            prev.Timeouts,
	}, diags
}

// pendingModel is the state saved the moment the backend accepts a create. It
// holds the id and no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	m := plan
	m.ID = types.StringValue(id)
	if m.Workers.IsUnknown() { // autoscaling chooses it
		m.Workers = types.Int64Null()
	}
	m.Endpoint = types.StringNull()
	m.VolumeStorageGB = types.Int64Null()
	m.KubernetesVersion = types.StringNull()
	m.NetworkID = types.StringNull()
	m.NodePlanID = types.StringNull()
	m.Node = types.ObjectNull(nodeAttrTypes)
	m.Nodes = types.Int64Null()
	m.ObservedState = types.StringNull()
	m.InSync = types.BoolNull()
	m.AvailableUpgradeIDs = types.ListNull(types.StringType)
	m.KubeConfig = types.StringNull()
	m.CreatedAt = types.StringNull()
	m.UpdatedAt = types.StringNull()
	return m
}

// upgradeIDs lists the ids of the versions the cluster can move to now.
func upgradeIDs(k *client.KubernetesCluster) []string {
	ids := make([]string, 0, len(k.AvailableUpgrades))
	for _, v := range k.AvailableUpgrades {
		ids = append(ids, v.ID)
	}
	return ids
}

// hasKubeConfig reports whether the state already holds a kubeconfig.
func hasKubeConfig(m model) bool {
	return !m.KubeConfig.IsNull() && !m.KubeConfig.IsUnknown() && m.KubeConfig.ValueString() != ""
}

func nullIfUnknown(v types.String) types.String {
	if v.IsUnknown() {
		return types.StringNull()
	}
	return v
}
