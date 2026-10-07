package kubernetescluster

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// Limits the backend enforces.
const (
	minWorkers = 1
	maxWorkers = 10
	maxCIDRs   = 20
)

// namePattern is the backend's rule: 1 to 40 lowercase letters, digits and
// hyphens, starting with a letter and not ending with a hyphen.
var namePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// nameValidator checks the cluster name against namePattern.
type nameValidator struct{}

func (nameValidator) Description(context.Context) string {
	return "must be 1 to 40 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen"
}

func (v nameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (nameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !namePattern.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Kubernetes cluster name",
			fmt.Sprintf("%q is not a valid name: use 1 to 40 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen, for example \"prod-web\".", req.ConfigValue.ValueString()))
	}
}

// controlNodesValidator allows 1, or 3 for a highly available control plane.
type controlNodesValidator struct{}

func (controlNodesValidator) Description(context.Context) string {
	return "must be 1, or 3 for a highly available control plane"
}

func (v controlNodesValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (controlNodesValidator) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := req.ConfigValue.ValueInt64(); n != 1 && n != 3 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid control_nodes",
			fmt.Sprintf("control_nodes must be 1, or 3 where the zone offers a highly available control plane, got %d.", n))
	}
}

// replaceUnlessImported replaces the cluster when node_plan differs from the
// state, but not when the state holds none. The API reports node_plan_id, not
// the slug, so an imported cluster has none in state, and replacing it for
// that would destroy the cluster to fix a gap in our own state.
func replaceUnlessImported(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull() && !req.ConfigValue.Equal(req.StateValue)
}

// checkUpgrade stops at plan time a kubernetes_version_id change the platform
// would refuse: the new version must be one of the cluster's available
// upgrades (the next patch or the next minor version). Downgrades are never
// available. It does nothing on create, on destroy, or while the new id is
// unknown.
func checkUpgrade(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var planned, current types.String
	var upgrades types.List
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("kubernetes_version_id"), &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("kubernetes_version_id"), &current)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("available_upgrade_ids"), &upgrades)...)
	if resp.Diagnostics.HasError() || planned.IsUnknown() || planned.IsNull() || planned.Equal(current) || upgrades.IsNull() || upgrades.IsUnknown() {
		return
	}
	var ids []string
	resp.Diagnostics.Append(upgrades.ElementsAs(ctx, &ids, false)...)
	if slices.Contains(ids, planned.ValueString()) {
		return
	}
	available := "None is available now."
	if len(ids) > 0 {
		available = "Available now: " + strings.Join(ids, ", ") + "."
	}
	resp.Diagnostics.AddAttributeError(path.Root("kubernetes_version_id"), "Not an available upgrade",
		fmt.Sprintf("The cluster runs %s and cannot move to %s. An upgrade must be one of the cluster's available_upgrade_ids: the next patch or the next minor version, one minor at a time, and never down. %s To change to an unrelated version, create a new cluster.",
			current.ValueString(), planned.ValueString(), available))
}

// cidrSetValidator allows at most maxCIDRs IPv4 CIDRs with their host bits
// clear, the backend's rule for api_allowed_cidrs.
type cidrSetValidator struct{}

func (cidrSetValidator) Description(context.Context) string {
	return fmt.Sprintf("must be at most %d IPv4 CIDRs, each with its host bits clear", maxCIDRs)
}

func (v cidrSetValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (cidrSetValidator) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > maxCIDRs {
		resp.Diagnostics.AddAttributeError(req.Path, "Too many CIDRs",
			fmt.Sprintf("api_allowed_cidrs takes at most %d IPv4 CIDRs, got %d.", maxCIDRs, len(elems)))
	}
	for _, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		p, err := netip.ParsePrefix(s.ValueString())
		if err != nil || !p.Addr().Is4() || p.Masked() != p {
			resp.Diagnostics.AddAttributeError(req.Path.AtSetValue(s), "Invalid CIDR",
				fmt.Sprintf("%q is not an IPv4 CIDR with its host bits clear, for example \"203.0.113.0/24\" (or \"203.0.113.7/32\" for one address).", s.ValueString()))
		}
	}
}

// validateConfig checks the rules that tie attributes together, at plan time:
//   - autoscaling on needs min_workers and max_workers, min no more than max,
//     and no workers, because the autoscaler owns the count;
//   - autoscaling off (or absent) needs workers, and takes no bounds;
//   - api_allowed_cidrs is for a VPC zone, so it needs subnet_id.
//
// Unknown values are skipped: they are checked again once known.
func validateConfig(ctx context.Context, cfg tfsdk.Config, diags *diag.Diagnostics) {
	var workers types.Int64
	var subnet types.String
	var cidrs types.Set
	var auto types.Object
	diags.Append(cfg.GetAttribute(ctx, path.Root("workers"), &workers)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("subnet_id"), &subnet)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("api_allowed_cidrs"), &cidrs)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("autoscaling"), &auto)...)
	if diags.HasError() {
		return
	}
	validateScaling(ctx, auto, workers, diags)
	if subnet.IsNull() && !cidrs.IsNull() && !cidrs.IsUnknown() && len(cidrs.Elements()) > 0 {
		diags.AddAttributeError(path.Root("api_allowed_cidrs"), "api_allowed_cidrs needs a VPC zone",
			"The API server allow-list is offered only for clusters in a VPC zone (with subnet_id). In a standard zone remove api_allowed_cidrs: any address can reach the API server, which still requires the kubeconfig's credential.")
	}
}

// validateScaling checks workers against the autoscaling object.
func validateScaling(ctx context.Context, auto types.Object, workers types.Int64, diags *diag.Diagnostics) {
	if auto.IsUnknown() {
		return
	}
	var a autoscalingModel
	if !auto.IsNull() {
		diags.Append(auto.As(ctx, &a, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
		if diags.HasError() || a.Enabled.IsUnknown() {
			return
		}
	}
	autoPath := path.Root("autoscaling")
	if !a.Enabled.ValueBool() {
		if workers.IsNull() {
			diags.AddAttributeError(path.Root("workers"), "Missing workers",
				"Set workers (1 to 10), or turn the autoscaler on with autoscaling = { enabled = true, min_workers = ..., max_workers = ... }.")
		}
		if !a.MinWorkers.IsNull() || !a.MaxWorkers.IsNull() {
			diags.AddAttributeError(autoPath, "Autoscaling bounds without autoscaling",
				"min_workers and max_workers apply only with enabled = true. Remove them, or set enabled = true.")
		}
		return
	}
	if a.MinWorkers.IsNull() || a.MaxWorkers.IsNull() {
		diags.AddAttributeError(autoPath, "Missing autoscaling bounds",
			"With enabled = true, set both min_workers and max_workers (1 to 10).")
		return
	}
	if !a.MinWorkers.IsUnknown() && !a.MaxWorkers.IsUnknown() && a.MinWorkers.ValueInt64() > a.MaxWorkers.ValueInt64() {
		diags.AddAttributeError(autoPath.AtName("min_workers"), "Invalid autoscaling range",
			fmt.Sprintf("min_workers (%d) must not be more than max_workers (%d).", a.MinWorkers.ValueInt64(), a.MaxWorkers.ValueInt64()))
	}
	if !workers.IsNull() {
		diags.AddAttributeError(path.Root("workers"), "workers is set by the autoscaler",
			"While autoscaling is enabled the autoscaler sets the worker count between min_workers and max_workers, and the API refuses a fixed count. Remove workers: the cluster starts at min_workers, and the current count is reported in workers. To go back to a fixed count, remove autoscaling (or set enabled = false) and set workers.")
	}
}

// planWorkers marks workers unknown in an update plan when the autoscaler owns
// it and the cluster is about to change. A new range clamps the count, and the
// autoscaler can move it while a change runs, so the value kept from state
// would not match what the apply reads back. With nothing to change the state
// value stays, so the autoscaler's moves never show as a diff.
func planWorkers(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	var configured types.Int64
	var auto types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("workers"), &configured)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("autoscaling"), &auto)...)
	if resp.Diagnostics.HasError() || !configured.IsNull() {
		return
	}
	if auto.IsUnknown() || autoscalingEnabled(ctx, auto) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("workers"), types.Int64Unknown())...)
	}
}
