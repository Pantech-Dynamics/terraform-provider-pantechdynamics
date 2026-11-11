// Package registrynamespace implements the
// pantechdynamics_container_registry_namespace resource: the organization's
// one namespace in the Pantech container registry.
package registrynamespace

import (
	"context"
	"fmt"
	"slices"

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

// namespaceAPI is the part of the API client this resource needs. It is
// defined here, by the consumer, so tests can fake it.
type namespaceAPI interface {
	GetRegistryNamespace(ctx context.Context) (*client.RegistryNamespace, error)
	SetRegistryNamespace(ctx context.Context, req client.SetRegistryNamespaceRequest) (*client.RegistryNamespace, error)
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
	_ resource.ResourceWithModifyPlan  = &Resource{}
)

// Resource manages the organization's registry namespace.
type Resource struct {
	api namespaceAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// model maps the resource's attributes to Go.
type model struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	CreateOnPush      types.Bool   `tfsdk:"create_on_push"`
	RegistryHost      types.String `tfsdk:"registry_host"`
	RepositoryCount   types.Int64  `tfsdk:"repository_count"`
	StorageBytes      types.Int64  `tfsdk:"storage_bytes"`
	StorageLimitBytes types.Int64  `tfsdk:"storage_limit_bytes"`
	RepositoryLimit   types.Int64  `tfsdk:"repository_limit"`
	CreatedAt         types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_container_registry_namespace.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_namespace"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Your organization's namespace in the Pantech container registry: images are <registry_host>/<name>/<repository>:<tag>. " +
			"An organization has exactly one namespace. Its name can change only while it holds no repository; after that a rename is refused (REGISTRY_NAMESPACE_LOCKED). " +
			"A namespace cannot be deleted, so destroying this resource only removes it from Terraform state. Only an API key made by an owner or admin can change it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The namespace's name, which is also its id.",
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The namespace, globally unique: 3 to 30 characters, lower-case letters, digits and single hyphens, starting with a letter. " +
					"Reserved words such as \"library\", \"pantech\" and \"admin\" are refused. Changing it renames the namespace in place, which changes every image path, and is refused once a repository has ever been created in it.",
				Required:   true,
				Validators: []validator.String{namespaceName{}},
			},
			"create_on_push": schema.BoolAttribute{
				Description: "Create a repository on the first push of an image to it. Defaults to true. With false, repositories must be created first (pantechdynamics_container_repository).",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"registry_host": schema.StringAttribute{
				Description:   "Host name of the registry, for docker login and image names, such as \"registry.pantechdynamics.com\".",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"repository_count": schema.Int64Attribute{
				Description: "How many repositories the namespace holds.",
				Computed:    true,
			},
			"storage_bytes": schema.Int64Attribute{
				Description: "Storage used by the namespace's images, in bytes. Layers shared between images are counted once.",
				Computed:    true,
			},
			"storage_limit_bytes": schema.Int64Attribute{
				Description: "The most storage the namespace may use, in bytes. Pushes past it are refused (REGISTRY_STORAGE_LIMIT_REACHED).",
				Computed:    true,
			},
			"repository_limit": schema.Int64Attribute{
				Description: "The most repositories the namespace may hold.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the namespace was claimed, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(namespaceAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ModifyPlan warns before a rename, which changes every image path, and
// before a destroy, which leaves the namespace in place.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		return
	}
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if req.Plan.Raw.IsNull() {
		resp.Diagnostics.AddWarning("Destroy does not delete the namespace", destroyWarning(state.Name.ValueString()))
		return
	}
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Name.IsUnknown() || plan.Name.Equal(state.Name) {
		return
	}
	resp.Diagnostics.AddWarning("Renaming the registry namespace",
		fmt.Sprintf("Applying this plan renames namespace %q to %q, which changes the path of every image. The registry refuses the rename once the namespace holds a repository.",
			state.Name.ValueString(), plan.Name.ValueString()))
}

// Create claims the namespace. Claiming the name the organization already
// has is accepted, which adopts it.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.set(ctx, "Error claiming registry namespace", plan, &resp.State, &resp.Diagnostics)
}

// Read refreshes state from the API. A namespace that is no longer set is
// removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	ns, err := r.api.GetRegistryNamespace(ctx)
	if client.HasCode(err, client.CodeRegistryNamespaceNotSet) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading registry namespace", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ns))...)
}

// Update renames the namespace or changes create_on_push.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.set(ctx, "Error updating registry namespace", plan, &resp.State, &resp.Diagnostics)
}

// Delete does not delete anything: the API has no way to release a
// namespace. It leaves Terraform state and stays the organization's.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.AddWarning("Registry namespace removed from Terraform state only", destroyWarning(state.Name.ValueString()))
}

// ImportState adopts the organization's namespace by its name. The name must
// be the one the organization holds, so a typo fails here.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ns, err := r.api.GetRegistryNamespace(ctx)
	if client.HasCode(err, client.CodeRegistryNamespaceNotSet) {
		resp.Diagnostics.AddError("Cannot import registry namespace", "Your organization has not claimed a registry namespace yet. Create one with this resource instead.")
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Cannot import registry namespace", err, nil)
		return
	}
	if ns.Name != req.ID {
		resp.Diagnostics.AddError("Cannot import registry namespace", fmt.Sprintf("Your organization's namespace is %q, not %q.", ns.Name, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ns))...)
}

// set sends the PUT and saves the answer. On a refused rename it names the
// namespace the organization holds.
func (r *Resource) set(ctx context.Context, summary string, plan model, st *tfsdk.State, diags *diag.Diagnostics) {
	createOnPush := plan.CreateOnPush.ValueBool()
	ns, err := r.api.SetRegistryNamespace(ctx, client.SetRegistryNamespaceRequest{Name: plan.Name.ValueString(), CreateOnPush: &createOnPush})
	if client.HasCode(err, client.CodeRegistryNamespaceLocked) {
		diags.AddAttributeError(path.Root("name"), "Registry namespace cannot be renamed", r.lockedDetail(ctx, err))
		return
	}
	if err != nil {
		resourcekit.AddHintedAPIError(diags, summary, err, attributeFor, hints)
		return
	}
	diags.Append(st.Set(ctx, fromAPIResponse(ns))...)
}

// lockedDetail explains REGISTRY_NAMESPACE_LOCKED with the current name, when
// it can be read.
func (r *Resource) lockedDetail(ctx context.Context, err error) string {
	detail := err.Error() + "\n\nThe namespace already holds repositories, so its name can no longer change."
	if ns, getErr := r.api.GetRegistryNamespace(ctx); getErr == nil {
		detail += fmt.Sprintf(" Your organization's namespace is %q: set name = %q", ns.Name, ns.Name)
		if ns.RepositoryCount > 0 {
			detail += fmt.Sprintf(" (it holds %d repositories)", ns.RepositoryCount)
		}
		detail += "."
	}
	return detail
}

func fromAPIResponse(ns *client.RegistryNamespace) model {
	return model{
		ID:                types.StringValue(ns.Name),
		Name:              types.StringValue(ns.Name),
		CreateOnPush:      types.BoolValue(ns.CreateOnPush),
		RegistryHost:      types.StringValue(ns.RegistryHost),
		RepositoryCount:   types.Int64Value(ns.RepositoryCount),
		StorageBytes:      types.Int64Value(ns.StorageBytes),
		StorageLimitBytes: types.Int64Value(ns.StorageLimitBytes),
		RepositoryLimit:   types.Int64Value(ns.RepositoryLimit),
		CreatedAt:         resourcekit.Timestamp(ns.CreatedAt),
	}
}

func destroyWarning(name string) string {
	return fmt.Sprintf("A registry namespace cannot be deleted, so destroying this resource only removes %q from Terraform state. "+
		"The namespace, its repositories and their images stay in your organization. Delete repositories with pantechdynamics_container_repository.", name)
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "create_on_push":
		return path.Root(field), true
	default:
		return path.Path{}, false
	}
}

// hints for the refusals a user can act on.
var hints = map[string]string{
	client.CodeRegistryNamespaceTaken: "Another organization already holds this namespace. Choose another name.",
	"INVALID_REGISTRY_NAMESPACE":      "Use 3 to 30 lower-case letters, digits and single hyphens, starting with a letter, and not a reserved word.",
	"FORBIDDEN":                       "Only owners and admins can change the registry namespace. Use an API key created by one.",
	"INSUFFICIENT_SCOPE":              "Changing the registry namespace needs an API key with the write scope.",
	client.CodeRegistryUnavailable:    "The container registry is not available on this platform yet.",
}

// reservedNamespaces are the reserved words the ADR names. The backend holds
// the full list; these are refused at plan time because they are certain.
var reservedNamespaces = []string{"library", "pantech", "admin", "v2", "_catalog"}

// namespaceName checks the namespace grammar at plan time.
type namespaceName struct{}

func (namespaceName) Description(context.Context) string {
	return "must be 3 to 30 lower-case letters, digits and single hyphens, starting with a letter"
}

func (v namespaceName) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (namespaceName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	name := req.ConfigValue.ValueString()
	if slices.Contains(reservedNamespaces, name) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid registry namespace", fmt.Sprintf("%q is reserved. Choose another name.", name))
		return
	}
	if !validNamespace(name) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid registry namespace",
			fmt.Sprintf("The namespace must be 3 to 30 lower-case letters, digits and single hyphens, starting with a letter, got %q.", name))
	}
}

func validNamespace(name string) bool {
	if len(name) < 3 || len(name) > 30 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && name[i-1] != '-':
		default:
			return false
		}
	}
	return true
}
