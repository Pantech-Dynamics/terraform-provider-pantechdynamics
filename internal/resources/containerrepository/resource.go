// Package containerrepository implements the
// pantechdynamics_container_repository resource.
package containerrepository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "crr_"
	kind     = "container repository"

	maxDescriptionLength = 255
	maxKeepPattern       = 128
)

// repositoryAPI is the part of the API client this resource needs. It is
// defined here, by the consumer, so tests can fake it.
type repositoryAPI interface {
	CreateRepository(ctx context.Context, req client.CreateRepositoryRequest) (*client.Repository, error)
	GetRepository(ctx context.Context, id string) (*client.Repository, error)
	ListRepositories(ctx context.Context, q string) ([]client.Repository, error)
	UpdateRepository(ctx context.Context, id string, req client.UpdateRepositoryRequest) (*client.Repository, error)
	DeleteRepository(ctx context.Context, id string) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one repository in the organization's namespace.
type Resource struct {
	api repositoryAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// Metadata sets the type name: pantechdynamics_container_repository.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_repository"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A repository in your organization's container registry namespace (pantechdynamics_container_registry_namespace), such as acme/api/server. " +
			"Push to it with docker push <full_name>:<tag> after docker login. **Destroying it deletes every image in it.** At most 100 repositories per organization; only an API key made by an owner or admin can change them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the repository, starting with crr_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Name inside the namespace, such as \"api/server\": lower-case path components of letters and digits, with single \".\", \"_\" or \"-\" between them, at most 255 characters. Changing it replaces the repository, and deletes its images.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.RepositoryName()},
			},
			"description": schema.StringAttribute{
				Description: "A note for yourself, at most 255 characters. Defaults to \"\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Validators:  []validator.String{resourcekit.MaxLength(maxDescriptionLength)},
			},
			"visibility": schema.StringAttribute{
				Description: "\"private\" (only your organization and its registry tokens can pull) or \"public\" (anyone can pull, without logging in). Defaults to \"private\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("private"),
				Validators:  []validator.String{resourcekit.OneOf("private", "public")},
			},
			"immutable_tags": schema.BoolAttribute{
				Description: "When true, a tag once pushed cannot be moved to another image or deleted; a push that moves one is reverted. Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"retention": schema.SingleNestedAttribute{
				Description: "Automatic cleanup, run daily. Leave it out for none; set at least one rule inside it. Tags matching keep_pattern are never removed by it.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"keep_last": schema.Int64Attribute{
						Description: "Keep only the newest this many tags, 1 to 1000.",
						Optional:    true,
						Validators:  []validator.Int64{resourcekit.IntBetween(1, 1000)},
					},
					"untagged_after_days": schema.Int64Attribute{
						Description: "Delete untagged images older than this many days, 1 to 365.",
						Optional:    true,
						Validators:  []validator.Int64{resourcekit.IntBetween(1, 365)},
					},
					"keep_pattern": schema.StringAttribute{
						Description: "A glob such as \"v*\" or \"release-*\", at most 128 characters: matching tags are always kept.",
						Optional:    true,
						Validators:  []validator.String{resourcekit.MaxLength(maxKeepPattern)},
					},
				},
			},
			"namespace": schema.StringAttribute{
				Description:   "The namespace the repository is in.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"full_name": schema.StringAttribute{
				Description:   "The image name to tag and push, such as \"registry.pantechdynamics.com/acme/api/server\".",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tag_count": schema.Int64Attribute{
				Description: "How many tags the repository holds.",
				Computed:    true,
			},
			"storage_bytes": schema.Int64Attribute{
				Description: "Storage the repository's images use, in bytes.",
				Computed:    true,
			},
			"last_pushed_at": schema.StringAttribute{
				Description: "When an image was last pushed, in RFC 3339 UTC, to the second. Null until the first push.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "\"active\", \"deleting\" or \"suspended\" (an operator suspended it; it cannot change).",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the repository was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(repositoryAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ValidateConfig refuses an empty retention block: the backend stores it as
// no policy, and state would then never match the configuration.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m model
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Retention == nil {
		return
	}
	ret := m.Retention
	if ret.KeepLast.IsNull() && ret.UntaggedAfterDays.IsNull() && ret.KeepPattern.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("retention"), "Empty retention policy",
			"Set at least one of keep_last, untagged_after_days and keep_pattern, or leave the retention block out.")
	}
}

// Create adds the repository. It is ready at once: there is nothing to wait
// for.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	repo, err := r.api.CreateRepository(ctx, toCreateRequest(plan))
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating container repository", err, attributeFor, createHints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(repo, plan.Timeouts))...)
}

// Read refreshes state from the API. A repository deleted, or being deleted,
// outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	repo, err := r.api.GetRepository(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && repo.Status == client.RepositoryDeleting) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading container repository", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(repo, state.Timeouts))...)
}

// Update changes what differs, in place.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, changed := toUpdateRequest(state, plan)
	if !changed {
		// Only the timeouts changed: nothing to send.
		state.Timeouts = plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	repo, err := r.api.UpdateRepository(ctx, state.ID.ValueString(), body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating container repository", err, attributeFor, updateHints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(repo, plan.Timeouts))...)
}

// Delete removes the repository and its images, and waits until it is gone.
// One already gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, state.Timeouts.Delete, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	err := r.api.DeleteRepository(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting container repository", err, deleteHints)
		return
	}
	err = r.api.WaitUntil(ctx, kind+" "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		_, err := r.api.GetRepository(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting container repository", kind, id, err)
	}
}

// ImportState adopts a repository by id (crr_...) or by its name in the
// namespace, such as "api/server".
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if !strings.HasPrefix(id, idPrefix) {
		if !resourcekit.ValidRepositoryName(id) {
			resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("Expected a repository id (crr_...) or a repository name such as \"api/server\", got %q.", id))
			return
		}
		repo, err := r.findByName(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Cannot import container repository", err.Error())
			return
		}
		id = repo.ID
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// findByName returns the repository with exactly this name.
func (r *Resource) findByName(ctx context.Context, name string) (*client.Repository, error) {
	repos, err := r.api.ListRepositories(ctx, name)
	if err != nil {
		return nil, err
	}
	for i := range repos {
		if repos[i].Name == name {
			return &repos[i], nil
		}
	}
	return nil, fmt.Errorf("the namespace has no repository named %q", name)
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "description", "visibility", "immutable_tags", "retention":
		return path.Root(field), true
	}
	if sub, ok := strings.CutPrefix(field, "retention."); ok && (sub == "keep_last" || sub == "untagged_after_days" || sub == "keep_pattern") {
		return path.Root("retention").AtName(sub), true
	}
	return path.Path{}, false
}

// Hints for the refusals a user can act on.
var (
	createHints = map[string]string{
		client.CodeRepositoryExists:        "The namespace already has a repository with this name (a first push creates one when create_on_push is on). Import it with `terraform import` by its name.",
		client.CodeRegistryNamespaceNotSet: "Claim a namespace first with pantechdynamics_container_registry_namespace, and make this resource depend on it.",
		"REPOSITORY_LIMIT_REACHED":         "Your organization already has the most repositories it may have (100). Delete one you no longer use.",
		"INVALID_REPOSITORY_NAME":          "Use lower-case path components such as \"api/server\".",
		"FORBIDDEN":                        "Only owners and admins can manage repositories. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":               "Managing repositories needs an API key with the write scope.",
		client.CodeRegistryUnavailable:     "The container registry is not available on this platform yet.",
		"REGISTRY_STORAGE_LIMIT_REACHED":   "Your organization has used all its registry storage. Delete images or repositories you no longer need.",
	}
	updateHints = map[string]string{
		"REPOSITORY_SUSPENDED":         "An operator suspended this repository, so it cannot change. Contact support.",
		"FORBIDDEN":                    "Only owners and admins can manage repositories. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":           "Managing repositories needs an API key with the write scope.",
		client.CodeRegistryUnavailable: "The container registry is not available on this platform yet.",
	}
	deleteHints = map[string]string{
		"REPOSITORY_SUSPENDED":         "An operator suspended this repository, so it cannot be deleted. Contact support.",
		"INSUFFICIENT_DELETE_SCOPE":    "Deleting a repository needs an API key with the delete scope.",
		"INSUFFICIENT_SCOPE":           "Deleting a repository needs an API key with the delete scope.",
		"FORBIDDEN":                    "Only owners and admins can manage repositories. Use an API key created by one.",
		client.CodeRegistryUnavailable: "The container registry is not available on this platform yet.",
	}
)
