// Package kubernetesaddon implements the pantechdynamics_kubernetes_addon
// resource.
package kubernetesaddon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const kind = "Kubernetes add-on"

// defaultTimeout bounds each wait for a toggle.
const defaultTimeout = 15 * time.Minute

// addonNames are the add-ons the platform manages.
var addonNames = []string{"metrics-server", "csi-driver", "ingress-controller"}

// addonAPI is the part of the API client this resource needs, defined here so
// tests can fake it.
type addonAPI interface {
	ListKubernetesAddons(ctx context.Context, clusterID string) ([]client.KubernetesAddon, error)
	ToggleKubernetesAddon(ctx context.Context, clusterID, name string, req client.ToggleKubernetesAddonRequest) (*client.OperationReference, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one add-on of a cluster.
type Resource struct {
	api addonAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

type model struct {
	ID        types.String   `tfsdk:"id"`
	ClusterID types.String   `tfsdk:"cluster_id"`
	Name      types.String   `tfsdk:"name"`
	Enabled   types.Bool     `tfsdk:"enabled"`
	Status    types.String   `tfsdk:"status"`
	Version   types.String   `tfsdk:"version"`
	Timeouts  timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the type name: pantechdynamics_kubernetes_addon.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_addon"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Turns a managed add-on of a pantechdynamics_kubernetes_cluster on or off: metrics-server (the cluster's own metrics-server, for kubectl top and the Horizontal Pod Autoscaler), csi-driver (persistent volumes on the node's own disk, StorageClass local-path; the data stays on that node) or ingress-controller (ingress-nginx, the default IngressClass, served on 80 and 443 of the cluster's public address in a VPC zone). A cluster starts with metrics-server and csi-driver on, and the platform keeps the choice applied. Create and update wait until the cluster runs (or has stopped running) the add-on. Destroying the resource turns the add-on off.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "\"<cluster_id>/<name>\".",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Description:   "Id of the cluster, starting with k8s_. It must be running. Changing it replaces the resource.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.IDPrefix("Kubernetes cluster", "k8s_")},
			},
			"name": schema.StringAttribute{
				Description:   "The add-on: \"metrics-server\", \"csi-driver\" or \"ingress-controller\". Changing it replaces the resource.",
				Required:      true,
				PlanModifiers: replaceStr,
				Validators:    []validator.String{resourcekit.OneOf(addonNames...)},
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the add-on is on. Defaults to true. Changes in place.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"status":   schema.StringAttribute{Computed: true, Description: "What the cluster runs of the add-on: \"active\", \"installing\", \"removing\", \"disabled\", or \"unavailable\" (the cluster has no such component)."},
			"version":  schema.StringAttribute{Computed: true, Description: "The add-on's version, once the platform reports one."},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(addonAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create sets the add-on to enabled and waits for the change.
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
	if !r.toggle(ctx, plan, "Error setting Kubernetes add-on", &resp.Diagnostics) {
		return
	}
	r.settle(ctx, plan, "Error setting Kubernetes add-on", &resp.Diagnostics)
	r.refresh(ctx, plan, &resp.State, &resp.Diagnostics)
}

// Read refreshes state. An add-on the cluster no longer lists, or a cluster
// that is gone, is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.find(ctx, state.ClusterID.ValueString(), state.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading Kubernetes add-on", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, a))...)
}

// Update turns the add-on on or off.
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
	if !plan.Enabled.Equal(state.Enabled) {
		if !r.toggle(ctx, plan, "Error changing Kubernetes add-on", &resp.Diagnostics) {
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
		r.settle(ctx, plan, "Error changing Kubernetes add-on", &resp.Diagnostics)
	}
	r.refresh(ctx, plan, &resp.State, &resp.Diagnostics)
}

// Delete turns the add-on off. A cluster already gone counts as success.
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
	off := state
	off.Enabled = types.BoolValue(false)
	var d diag.Diagnostics
	if !r.toggle(ctx, off, "Error turning Kubernetes add-on off", &d) {
		resp.Diagnostics.Append(d...)
	}
}

// ImportState adopts an add-on by "<cluster_id>/<name>".
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clusterID, name, ok := strings.Cut(req.ID, "/")
	if !ok || !strings.HasPrefix(clusterID, "k8s_") || name == "" {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("Expected \"<cluster_id>/<name>\", such as \"k8s_1/metrics-server\", got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), clusterID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// toggle sends the add-on's enabled setting and waits for it. A cluster that
// is gone is not an error when turning the add-on off.
func (r *Resource) toggle(ctx context.Context, m model, summary string, diags *diag.Diagnostics) bool {
	clusterID, name := m.ClusterID.ValueString(), m.Name.ValueString()
	ref, err := r.api.ToggleKubernetesAddon(ctx, clusterID, name, client.ToggleKubernetesAddonRequest{Enabled: m.Enabled.ValueBool()})
	if errors.Is(err, client.ErrNotFound) && !m.Enabled.ValueBool() {
		return true
	}
	if err != nil {
		resourcekit.AddAPIError(diags, summary, err, nil)
		return false
	}
	if ref.OperationID == "" {
		return true
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, nil); err != nil {
		resourcekit.AddWaitError(diags, summary, kind, clusterID+"/"+name, err)
		return false
	}
	return true
}

// errAddonUnavailable ends a wait on an add-on the cluster cannot run.
var errAddonUnavailable = errors.New("the cluster has no such component (status unavailable)")

// settle waits until the cluster runs the add-on as asked: active when on,
// disabled when off.
func (r *Resource) settle(ctx context.Context, m model, summary string, diags *diag.Diagnostics) {
	clusterID, name := m.ClusterID.ValueString(), m.Name.ValueString()
	want := client.AddonDisabled
	if m.Enabled.ValueBool() {
		want = client.AddonActive
	}
	err := r.api.WaitUntil(ctx, "add-on "+name+" to be "+want, func(ctx context.Context) (bool, error) {
		a, err := r.find(ctx, clusterID, name)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // recorded, not listed yet
		}
		if err != nil {
			return false, err
		}
		if a.Status == client.AddonUnavailable {
			return false, errAddonUnavailable
		}
		return a.Status == want, nil
	})
	if err != nil {
		resourcekit.AddWaitError(diags, summary, kind, clusterID+"/"+name, err)
	}
}

// find returns the add-on from the cluster's list, or ErrNotFound.
func (r *Resource) find(ctx context.Context, clusterID, name string) (*client.KubernetesAddon, error) {
	addons, err := r.api.ListKubernetesAddons(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	for i := range addons {
		if addons[i].Name == name {
			return &addons[i], nil
		}
	}
	return nil, fmt.Errorf("add-on %s: %w", name, client.ErrNotFound)
}

// refresh reads the add-on and stores it. The platform may not list an
// add-on it has only recorded, so the configured values are kept then.
func (r *Resource) refresh(ctx context.Context, prev model, st *tfsdk.State, diags *diag.Diagnostics) {
	a, err := r.find(ctx, prev.ClusterID.ValueString(), prev.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		m := prev
		m.ID = types.StringValue(prev.ClusterID.ValueString() + "/" + prev.Name.ValueString())
		m.Status, m.Version = types.StringNull(), types.StringNull()
		diags.Append(st.Set(ctx, m)...)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(diags, "Error reading Kubernetes add-on after the change", err, nil)
		return
	}
	diags.Append(st.Set(ctx, fromAPIResponse(prev, a))...)
}

// fromAPIResponse builds the state from the API object; prev supplies the
// cluster id when the API leaves it out, and the timeouts block.
func fromAPIResponse(prev model, a *client.KubernetesAddon) model {
	clusterID := a.ClusterID
	if clusterID == "" {
		clusterID = prev.ClusterID.ValueString()
	}
	return model{
		ID:        types.StringValue(clusterID + "/" + a.Name),
		ClusterID: types.StringValue(clusterID),
		Name:      types.StringValue(a.Name),
		Enabled:   types.BoolValue(a.Enabled),
		Status:    types.StringValue(a.Status),
		Version:   types.StringValue(a.Version),
		Timeouts:  prev.Timeouts,
	}
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
