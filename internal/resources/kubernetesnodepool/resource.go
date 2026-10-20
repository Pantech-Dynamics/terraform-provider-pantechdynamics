// Package kubernetesnodepool implements the pantechdynamics_kubernetes_node_pool
// resource.
package kubernetesnodepool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix   = "k8spool_"
	kind       = "node pool"
	minWorkers = 1
	maxWorkers = 10
)

// defaultTimeout bounds each wait: adding or removing workers builds or
// drains VMs, which takes longer than the 15 minutes simple resources allow.
const defaultTimeout = 30 * time.Minute

// poolAPI is the part of the API client this resource needs, defined here so
// tests can fake it.
type poolAPI interface {
	ListKubernetesNodePools(ctx context.Context, clusterID string) ([]client.KubernetesNodePool, error)
	CreateKubernetesNodePool(ctx context.Context, clusterID string, req client.CreateKubernetesNodePoolRequest) (*client.OperationReference, error)
	ScaleKubernetesNodePool(ctx context.Context, clusterID, poolID string, req client.ScaleKubernetesNodePoolRequest) (*client.OperationReference, error)
	DeleteKubernetesNodePool(ctx context.Context, clusterID, poolID string) (*client.OperationReference, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one node pool.
type Resource struct {
	api poolAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// Metadata sets the type name: pantechdynamics_kubernetes_node_pool.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_node_pool"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	taintType := types.ObjectType{AttrTypes: taintAttrTypes}
	resp.Schema = schema.Schema{
		Description: "A group of workers of a pantechdynamics_kubernetes_cluster with its own plan, size, Kubernetes labels and taints, for example bigger nodes for a database, or nodes only some workloads may use. Every worker is a VM of node_plan, joined with the pool's labels, the label pantech.io/node-pool=<name> and the pool's taints, so a workload picks the pool with a nodeSelector and tolerations. Each worker is billed as a VM of node_plan once it has joined the cluster (observed_workers). With autoscaling, the platform adds a worker when a pod the pool could run is waiting for room, and removes the newest one after 10 idle minutes. In a zone where every node has one fixed size (af-abj-2: 2 vCPU, 4 GB) the plan must be that size, or the create fails with KUBERNETES_PLAN_NOT_IN_ZONE. Create waits until every worker has joined. workers and autoscaling change in place; changing anything else replaces the pool.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the node pool, starting with k8spool_.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"cluster_id": schema.StringAttribute{
				Description:   "Id of the cluster, starting with k8s_. It must be running. Changing it replaces the pool.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IDPrefix("Kubernetes cluster", "k8s_")},
			},
			"name": schema.StringAttribute{
				Description:   "Name of the pool, 1 to 63 characters, unique in the cluster. Changing it replaces the pool.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.NameLength("node pool", 63)},
			},
			"node_plan": schema.StringAttribute{
				Description:   "Slug of the plan every worker of the pool uses, from the pantechdynamics_plans data source. The API reports the plan only by id (node_plan_id), so after an import this holds no value. Changing it replaces the pool.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(replaceUnlessImported, "Replaces the pool when the plan changes, but not after an import.", "Replaces the pool when the plan changes, but not after an import.")},
			},
			"workers": schema.Int64Attribute{
				Description:   "A fixed number of workers, 1 to 10. Required unless autoscaling is enabled; with it, the pool starts here (or at min_workers when unset) and the autoscaler then owns the count, which never causes a diff. Changes in place.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				Validators:    []validator.Int64{resourcekit.IntBetween(minWorkers, maxWorkers)},
			},
			"autoscaling": schema.SingleNestedAttribute{
				Description: "The autoscaler, which keeps the pool between min_workers and max_workers. Changes in place. Removing it (or enabled = false) turns it off at the current count, or at workers when set.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"enabled":     schema.BoolAttribute{Required: true, Description: "Whether the autoscaler runs. false is the same as leaving autoscaling out."},
					"min_workers": schema.Int64Attribute{Optional: true, Description: "Fewest workers, 1 to 10. Required when enabled.", Validators: []validator.Int64{resourcekit.IntBetween(minWorkers, maxWorkers)}},
					"max_workers": schema.Int64Attribute{Optional: true, Description: "Most workers, min_workers to 10. Required when enabled.", Validators: []validator.Int64{resourcekit.IntBetween(minWorkers, maxWorkers)}},
				},
			},
			"labels": schema.MapAttribute{
				Description:   "Kubernetes labels set on every node of the pool, for example { role = \"db\" }. Changing them replaces the pool.",
				ElementType:   types.StringType,
				Optional:      true,
				Computed:      true,
				Default:       mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})),
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"taints": schema.ListNestedAttribute{
				Description:   "Kubernetes taints set on every node of the pool, so only pods that tolerate them run there. Changing them replaces the pool.",
				Optional:      true,
				Computed:      true,
				Default:       listdefault.StaticValue(types.ListValueMust(taintType, []attr.Value{})),
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"key":    schema.StringAttribute{Required: true, Description: "The taint's key."},
					"value":  schema.StringAttribute{Required: true, Description: "The taint's value."},
					"effect": schema.StringAttribute{Required: true, Description: "\"NoSchedule\", \"PreferNoSchedule\" or \"NoExecute\".", Validators: []validator.String{resourcekit.OneOf("NoSchedule", "PreferNoSchedule", "NoExecute")}},
				}},
			},
			"node_plan_id": schema.StringAttribute{
				Description:   "Id of the plan every worker uses.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"status": schema.StringAttribute{
				Description: "What the cluster runs of the pool: \"provisioning\" (workers being built), \"ready\" (every worker has joined), \"updating\" (a scale in progress), \"deleting\", or \"unavailable\" (a pool made while node pools were a preview, with no node size: replace it).",
				Computed:    true,
			},
			"observed_workers": schema.Int64Attribute{
				Description: "The pool's workers that have joined the cluster. This is what is billed.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the pool was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// replaceUnlessImported replaces the pool when node_plan changes, except when
// the state holds none: after an import the configuration only fills it in.
func replaceUnlessImported(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull() && req.StateValue.ValueString() != ""
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(poolAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ValidateConfig needs a size: workers, or the autoscaler with its range.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m model
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Autoscaling.IsUnknown() || m.Workers.IsUnknown() {
		return
	}
	checkSize(ctx, m, &resp.Diagnostics)
}

// checkSize reports a pool size the backend would refuse.
func checkSize(ctx context.Context, m model, diags *diag.Diagnostics) {
	var a autoscalingModel
	on := false
	if !m.Autoscaling.IsNull() {
		diags.Append(m.Autoscaling.As(ctx, &a, objectAsOptions)...)
		on = a.Enabled.ValueBool()
	}
	switch {
	case !on && m.Workers.IsNull():
		diags.AddAttributeError(path.Root("workers"), "Missing workers", "Set workers, or enable autoscaling with min_workers and max_workers.")
	case on && (a.MinWorkers.IsNull() || a.MaxWorkers.IsNull()):
		diags.AddAttributeError(path.Root("autoscaling"), "Missing autoscaling range", "With autoscaling enabled, set min_workers and max_workers.")
	case on && !a.MinWorkers.IsUnknown() && !a.MaxWorkers.IsUnknown() && a.MinWorkers.ValueInt64() > a.MaxWorkers.ValueInt64():
		diags.AddAttributeError(path.Root("autoscaling"), "Invalid autoscaling range", "min_workers must not be more than max_workers.")
	case on && !m.Workers.IsNull() && (m.Workers.ValueInt64() < a.MinWorkers.ValueInt64() || m.Workers.ValueInt64() > a.MaxWorkers.ValueInt64()):
		diags.AddAttributeError(path.Root("workers"), "Workers outside the autoscaling range", "With autoscaling enabled, workers is the starting count and must be between min_workers and max_workers.")
	case !on && !m.Autoscaling.IsNull() && (!a.MinWorkers.IsNull() || !a.MaxWorkers.IsNull()):
		diags.AddAttributeError(path.Root("autoscaling"), "Unexpected autoscaling range", "With enabled = false, leave min_workers and max_workers out.")
	}
}

// Create adds the pool, saves its id straight away, then waits for the
// operation.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Create, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	body, diags := toCreateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	clusterID := plan.ClusterID.ValueString()
	ref, err := r.api.CreateKubernetesNodePool(ctx, clusterID, body)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error creating node pool", err, attributeFor)
		return
	}
	id := ref.ResourceID
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, id))...)
	if err := r.api.WaitForOperation(ctx, ref.OperationID, nil); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating node pool", kind, id, err)
		return
	}
	if err := r.api.WaitUntil(ctx, kind+" "+id+" to have every worker joined", r.joined(clusterID, id)); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating node pool", kind, id, err)
		r.refresh(ctx, plan, clusterID, id, &resp.State, &resp.Diagnostics)
		return
	}
	r.refresh(ctx, plan, clusterID, id, &resp.State, &resp.Diagnostics)
}

// errPoolUnavailable ends a wait on a pool the platform cannot build.
var errPoolUnavailable = errors.New("the platform cannot build this pool's workers (status unavailable): replace the pool")

// joined reports whether every worker of the pool has joined the cluster.
func (r *Resource) joined(clusterID, id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		p, err := r.find(ctx, clusterID, id)
		if err != nil {
			return false, err
		}
		if p.Status == client.NodePoolUnavailable {
			return false, errPoolUnavailable
		}
		return p.Status == client.NodePoolReady || p.ObservedWorkers == p.Workers, nil
	}
}

// Read refreshes state from the cluster's pool list: there is no single-pool
// read. A pool, or a cluster, gone outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.find(ctx, state.ClusterID.ValueString(), state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading node pool", err, nil)
		return
	}
	m, diags := fromAPIResponse(ctx, state, p)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Update scales the pool. The API takes the whole size each time.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Update, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	clusterID, id := state.ClusterID.ValueString(), state.ID.ValueString()
	prev := state
	prev.NodePlan, prev.Timeouts, prev.Autoscaling = plan.NodePlan, plan.Timeouts, plan.Autoscaling
	if plan.Workers.Equal(state.Workers) && plan.Autoscaling.Equal(state.Autoscaling) {
		resp.Diagnostics.Append(resp.State.Set(ctx, prev)...)
		return
	}
	body, diags := toScaleRequest(ctx, state, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref, err := r.api.ScaleKubernetesNodePool(ctx, clusterID, id, body)
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error scaling node pool", err, attributeFor)
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	if ref.OperationID != "" {
		if err := r.api.WaitForOperation(ctx, ref.OperationID, nil); err != nil {
			resourcekit.AddWaitError(&resp.Diagnostics, "Error scaling node pool", kind, id, err)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	r.refresh(ctx, prev, clusterID, id, &resp.State, &resp.Diagnostics)
}

// Delete removes the pool and waits until the cluster no longer lists it. A
// pool or cluster already gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, state.Timeouts.Delete, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	clusterID, id := state.ClusterID.ValueString(), state.ID.ValueString()
	ref, err := r.api.DeleteKubernetesNodePool(ctx, clusterID, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error deleting node pool", err, nil)
		return
	}
	gone := func(ctx context.Context) (bool, error) {
		_, err := r.find(ctx, clusterID, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, gone); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting node pool", kind, id, err)
	}
}

// ImportState adopts an existing pool by "<cluster_id>/<pool_id>".
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clusterID, poolID, ok := strings.Cut(req.ID, "/")
	if !ok || !strings.HasPrefix(clusterID, "k8s_") || !strings.HasPrefix(poolID, idPrefix) {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("Expected \"<cluster_id>/<pool_id>\", such as \"k8s_1/k8spool_1\", got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), clusterID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), poolID)...)
}

// find returns the pool from its cluster's list, or ErrNotFound when the pool
// or the cluster is gone.
func (r *Resource) find(ctx context.Context, clusterID, id string) (*client.KubernetesNodePool, error) {
	pools, err := r.api.ListKubernetesNodePools(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	for i := range pools {
		if pools[i].ID == id {
			return &pools[i], nil
		}
	}
	return nil, fmt.Errorf("node pool %s: %w", id, client.ErrNotFound)
}

// refresh reads the pool and stores it, keeping what only Terraform knows.
func (r *Resource) refresh(ctx context.Context, prev model, clusterID, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	p, err := r.find(ctx, clusterID, id)
	if err != nil {
		resourcekit.AddAPIError(diags, "Error reading node pool after the change", err, nil)
		return
	}
	m, d := fromAPIResponse(ctx, prev, p)
	diags.Append(d...)
	diags.Append(st.Set(ctx, m)...)
}

func withTimeout(ctx context.Context, get resourcekit.Timeout, diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, defaultTimeout)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}

// attributeFor maps a 422 field to a schema attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "workers", "labels", "taints":
		return path.Root(field), true
	case "plan_id":
		return path.Root("node_plan"), true
	case "autoscaling_enabled", "min_workers", "max_workers":
		return path.Root("autoscaling"), true
	}
	return path.Path{}, false
}
