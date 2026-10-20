package kubernetesnodepool

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
	ID          types.String   `tfsdk:"id"`
	ClusterID   types.String   `tfsdk:"cluster_id"`
	Name        types.String   `tfsdk:"name"`
	NodePlan    types.String   `tfsdk:"node_plan"`
	Workers     types.Int64    `tfsdk:"workers"`
	Autoscaling types.Object   `tfsdk:"autoscaling"`
	Labels      types.Map      `tfsdk:"labels"`
	Taints      types.List     `tfsdk:"taints"`
	NodePlanID  types.String   `tfsdk:"node_plan_id"`
	Status      types.String   `tfsdk:"status"`
	Observed    types.Int64    `tfsdk:"observed_workers"`
	CreatedAt   types.String   `tfsdk:"created_at"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

// autoscalingModel maps the autoscaling object. The bounds are null when it
// is off.
type autoscalingModel struct {
	Enabled    types.Bool  `tfsdk:"enabled"`
	MinWorkers types.Int64 `tfsdk:"min_workers"`
	MaxWorkers types.Int64 `tfsdk:"max_workers"`
}

// taintModel maps one taint.
type taintModel struct {
	Key    types.String `tfsdk:"key"`
	Value  types.String `tfsdk:"value"`
	Effect types.String `tfsdk:"effect"`
}

var autoscalingAttrTypes = map[string]attr.Type{
	"enabled":     types.BoolType,
	"min_workers": types.Int64Type,
	"max_workers": types.Int64Type,
}

var taintAttrTypes = map[string]attr.Type{
	"key":    types.StringType,
	"value":  types.StringType,
	"effect": types.StringType,
}

// size is a pool's workers and autoscaler range, as the API takes them.
type size struct {
	workers  int64
	enabled  bool
	min, max int64
}

// sizeOf reads the configured size. With the autoscaler on and workers unset,
// the pool starts at min_workers; the backend requires a count either way.
func sizeOf(ctx context.Context, m model) (size, diag.Diagnostics) {
	var s size
	var diags diag.Diagnostics
	if !m.Autoscaling.IsNull() && !m.Autoscaling.IsUnknown() {
		var a autoscalingModel
		diags = m.Autoscaling.As(ctx, &a, basetypes.ObjectAsOptions{})
		if a.Enabled.ValueBool() {
			s.enabled, s.min, s.max = true, a.MinWorkers.ValueInt64(), a.MaxWorkers.ValueInt64()
		}
	}
	switch {
	case !m.Workers.IsNull() && !m.Workers.IsUnknown():
		s.workers = m.Workers.ValueInt64()
	case s.enabled:
		s.workers = s.min
	}
	return s, diags
}

// toCreateRequest builds the create body.
func toCreateRequest(ctx context.Context, plan model) (client.CreateKubernetesNodePoolRequest, diag.Diagnostics) {
	s, diags := sizeOf(ctx, plan)
	req := client.CreateKubernetesNodePoolRequest{
		Name: plan.Name.ValueString(), PlanID: plan.NodePlan.ValueString(), Workers: s.workers,
		AutoscalingEnabled: s.enabled, MinWorkers: s.min, MaxWorkers: s.max,
	}
	if !plan.Labels.IsNull() && !plan.Labels.IsUnknown() {
		labels := map[string]string{}
		diags.Append(plan.Labels.ElementsAs(ctx, &labels, false)...)
		if len(labels) > 0 {
			req.Labels = labels
		}
	}
	if !plan.Taints.IsNull() && !plan.Taints.IsUnknown() {
		var taints []taintModel
		diags.Append(plan.Taints.ElementsAs(ctx, &taints, false)...)
		for _, t := range taints {
			req.Taints = append(req.Taints, client.KubernetesNodePoolTaint{Key: t.Key.ValueString(), Value: t.Value.ValueString(), Effect: t.Effect.ValueString()})
		}
	}
	return req, diags
}

// toScaleRequest builds the pool's whole new size. A workers count the plan
// leaves to the autoscaler keeps the current one, moved into the new range.
func toScaleRequest(ctx context.Context, state, plan model) (client.ScaleKubernetesNodePoolRequest, diag.Diagnostics) {
	s, diags := sizeOf(ctx, plan)
	if plan.Workers.IsUnknown() || plan.Workers.IsNull() {
		s.workers = state.Workers.ValueInt64()
		if s.enabled {
			s.workers = min(max(s.workers, s.min), s.max)
		}
	}
	return client.ScaleKubernetesNodePoolRequest{Workers: s.workers, AutoscalingEnabled: s.enabled, MinWorkers: s.min, MaxWorkers: s.max}, diags
}

// fromAPIResponse builds the state from the API object. prev supplies what
// only Terraform holds: node_plan (the API reports node_plan_id), the shape of
// an autoscaler that is off, and the timeouts block.
func fromAPIResponse(ctx context.Context, prev model, p *client.KubernetesNodePool) (model, diag.Diagnostics) {
	labels, diags := types.MapValueFrom(ctx, types.StringType, nonNilMap(p.Labels))
	taints := make([]attr.Value, 0, len(p.Taints))
	for _, t := range p.Taints {
		v, d := types.ObjectValue(taintAttrTypes, map[string]attr.Value{
			"key": types.StringValue(t.Key), "value": types.StringValue(t.Value), "effect": types.StringValue(t.Effect),
		})
		diags.Append(d...)
		taints = append(taints, v)
	}
	taintList, d := types.ListValue(types.ObjectType{AttrTypes: taintAttrTypes}, taints)
	diags.Append(d...)
	auto, d := autoscalingValue(ctx, prev.Autoscaling, p)
	diags.Append(d...)
	nodePlan := prev.NodePlan
	if nodePlan.IsUnknown() {
		nodePlan = types.StringNull()
	}
	return model{
		ID:          types.StringValue(p.ID),
		ClusterID:   types.StringValue(p.ClusterID),
		Name:        types.StringValue(p.Name),
		NodePlan:    nodePlan,
		Workers:     types.Int64Value(p.Workers),
		Autoscaling: auto,
		Labels:      labels,
		Taints:      taintList,
		NodePlanID:  types.StringValue(p.NodePlanID),
		Status:      types.StringValue(p.Status),
		Observed:    types.Int64Value(p.ObservedWorkers),
		CreatedAt:   resourcekit.Timestamp(p.CreatedAt),
		Timeouts:    prev.Timeouts,
	}, diags
}

// autoscalingValue is the autoscaling object: the range when it is on;
// otherwise null, or {enabled = false} when the configuration says so.
func autoscalingValue(ctx context.Context, prev types.Object, p *client.KubernetesNodePool) (types.Object, diag.Diagnostics) {
	if p.AutoscalingEnabled {
		return types.ObjectValue(autoscalingAttrTypes, map[string]attr.Value{
			"enabled": types.BoolValue(true), "min_workers": types.Int64Value(p.MinWorkers), "max_workers": types.Int64Value(p.MaxWorkers),
		})
	}
	if prev.IsNull() || prev.IsUnknown() {
		return types.ObjectNull(autoscalingAttrTypes), nil
	}
	var a autoscalingModel
	if diags := prev.As(ctx, &a, basetypes.ObjectAsOptions{}); diags.HasError() || a.Enabled.ValueBool() {
		return types.ObjectNull(autoscalingAttrTypes), diags
	}
	return types.ObjectValue(autoscalingAttrTypes, map[string]attr.Value{
		"enabled": types.BoolValue(false), "min_workers": types.Int64Null(), "max_workers": types.Int64Null(),
	})
}

// pendingModel is the state saved the moment the backend accepts a create.
func pendingModel(plan model, id string) model {
	m := plan
	m.ID = types.StringValue(id)
	if m.Workers.IsUnknown() {
		m.Workers = types.Int64Null()
	}
	if m.Labels.IsUnknown() {
		m.Labels = types.MapNull(types.StringType)
	}
	if m.Taints.IsUnknown() {
		m.Taints = types.ListNull(types.ObjectType{AttrTypes: taintAttrTypes})
	}
	m.NodePlanID = types.StringNull()
	m.Status = types.StringNull()
	m.Observed = types.Int64Null()
	m.CreatedAt = types.StringNull()
	return m
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

var objectAsOptions = basetypes.ObjectAsOptions{}
