// Package registrytoken implements the pantechdynamics_container_registry_token
// resource: a scoped credential for pulling or pushing images.
package registrytoken

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "prt_"
	kind     = "registry token"
)

// tokenAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type tokenAPI interface {
	CreateRegistryToken(ctx context.Context, req client.CreateRegistryTokenRequest) (*client.RegistryTokenWithSecret, error)
	ListRegistryTokens(ctx context.Context) ([]client.RegistryToken, error)
	DeleteRegistryToken(ctx context.Context, id string) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one registry token. Tokens cannot change, so every
// argument replaces it.
type Resource struct {
	api tokenAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// model maps the resource's attributes to Go.
type model struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Permission   types.String `tfsdk:"permission"`
	Repositories types.Set    `tfsdk:"repositories"`
	ExpiresAt    types.String `tfsdk:"expires_at"`
	Secret       types.String `tfsdk:"secret"`
	LastUsedAt   types.String `tfsdk:"last_used_at"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_container_registry_token.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_token"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A registry token: a credential for CI or a cluster to pull, or push, images, and nothing else (it cannot call the Pantech API). " +
			"Log in with docker login <registry_host> -u pantech -p <secret>. Tokens cannot change, so changing any argument replaces the token, which issues a new secret and revokes the old one. " +
			"**The secret is shown only when the token is created, so it is kept in Terraform state: treat the state as a secret.** " +
			"Destroying the resource revokes the token. At most 50 per organization; only an API key made by an owner or admin can manage them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the token, starting with prt_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "A name for the token, such as \"github-actions\". Changing it replaces the token.",
				Required:      true,
				PlanModifiers: replace,
			},
			"permission": schema.StringAttribute{
				Description:   "\"pull\" (read images) or \"push\" (push and pull). Changing it replaces the token.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.OneOf("pull", "push")},
			},
			"repositories": schema.SetAttribute{
				Description:   "The repositories (names in the namespace, such as \"api/server\") the token works for. Leave it out for every repository in the namespace. Changing it replaces the token.",
				Optional:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
				Validators:    []validator.Set{repositoryNames{}},
			},
			"expires_at": schema.StringAttribute{
				Description:   "When the token stops working, in RFC 3339, such as \"2027-11-10T00:00:00Z\". Leave it out for a token that does not expire. Changing it replaces the token.",
				Optional:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{rfc3339{}},
			},
			"secret": schema.StringAttribute{
				Description: "The token's secret (prt_..._...), the password for docker login. The API shows it only in the create response, so it exists only in Terraform state. " +
					"It is null after an import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"last_used_at": schema.StringAttribute{
				Description: "When the token was last used, in RFC 3339 UTC, to the second. Null until first use.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the token was created, in RFC 3339 UTC, to the second.",
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
	api, ok := req.ProviderData.(tokenAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create issues the token and saves it, with its one-time secret, straight
// away.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := toCreateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.api.CreateRegistryToken(ctx, body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating registry token", err, attributeFor, createHints)
		return
	}
	if created.Secret == nil {
		// A retried create returns the token without its secret, which only
		// the lost first response carried.
		resp.Diagnostics.AddWarning("Registry token secret not returned",
			"The registry token "+created.ID+" was created, but the response that carried its secret was lost, so secret is empty in state. "+
				"Replace the token (terraform apply -replace) to get a new secret.")
	}
	m, diags := fromAPIResponse(ctx, plan, &created.RegistryToken, types.StringPointerValue(created.Secret))
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Read refreshes state from the token list (there is no single read). A token
// revoked or deleted outside Terraform is removed from state, so the next
// plan issues a new one.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tok, err := r.find(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && tok.RevokedAt != nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading registry token", err, nil)
		return
	}
	m, diags := fromAPIResponse(ctx, state, tok, state.Secret)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Update is never asked to change anything: every argument replaces the
// token. It keeps the state as it is.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete revokes the token. One already gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.api.DeleteRegistryToken(ctx, state.ID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error revoking registry token", err, deleteHints)
	}
}

// ImportState adopts a token by id. Its secret cannot be read back, so it
// stays null; the token keeps working for whoever holds the secret.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.AddWarning("Registry token secret not imported",
		"The API never returns a registry token's secret after it is created, so secret is null in state. Replace the token to have Terraform hold a secret.")
}

// find returns the token from the list, or ErrNotFound.
func (r *Resource) find(ctx context.Context, id string) (*client.RegistryToken, error) {
	tokens, err := r.api.ListRegistryTokens(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tokens {
		if tokens[i].ID == id {
			return &tokens[i], nil
		}
	}
	return nil, fmt.Errorf("registry token %s: %w", id, client.ErrNotFound)
}

// toCreateRequest turns the plan into the create request. An unset
// repositories list means every repository.
func toCreateRequest(ctx context.Context, plan model) (client.CreateRegistryTokenRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	req := client.CreateRegistryTokenRequest{Name: plan.Name.ValueString(), Permission: plan.Permission.ValueString()}
	if !plan.Repositories.IsNull() && !plan.Repositories.IsUnknown() {
		diags.Append(plan.Repositories.ElementsAs(ctx, &req.Repositories, false)...)
		slices.Sort(req.Repositories)
	}
	if !plan.ExpiresAt.IsNull() && !plan.ExpiresAt.IsUnknown() {
		t, err := time.Parse(time.RFC3339, plan.ExpiresAt.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("expires_at"), "Invalid expiry", err.Error())
		}
		t = t.UTC()
		req.ExpiresAt = &t
	}
	return req, diags
}

// fromAPIResponse builds the state. The configured spelling of expires_at and
// repositories is kept when it means what the API reports, so a time written
// with an offset, or an empty list, does not show a change.
func fromAPIResponse(ctx context.Context, prev model, t *client.RegistryToken, secret types.String) (model, diag.Diagnostics) {
	var diags diag.Diagnostics
	if secret.IsUnknown() {
		secret = types.StringNull()
	}
	return model{
		ID:           types.StringValue(t.ID),
		Name:         types.StringValue(t.Name),
		Permission:   types.StringValue(t.Permission),
		Repositories: stateRepositories(ctx, prev, t.Repositories, &diags),
		ExpiresAt:    stateExpiry(prev.ExpiresAt, t.ExpiresAt),
		Secret:       secret,
		LastUsedAt:   resourcekit.Timestamp(t.LastUsedAt),
		CreatedAt:    resourcekit.Timestamp(t.CreatedAt),
	}, diags
}

// stateRepositories keeps the configured set (null or empty, for every
// repository) when it is the same as what the API reports.
func stateRepositories(ctx context.Context, prev model, reported []string, diags *diag.Diagnostics) types.Set {
	if !prev.Repositories.IsUnknown() {
		var configured []string
		if !prev.Repositories.IsNull() {
			diags.Append(prev.Repositories.ElementsAs(ctx, &configured, false)...)
		}
		if sameSet(configured, reported) {
			return prev.Repositories
		}
	}
	if len(reported) == 0 {
		return types.SetNull(types.StringType)
	}
	set, d := types.SetValueFrom(ctx, types.StringType, reported)
	diags.Append(d...)
	return set
}

// stateExpiry keeps the configured expiry when it is the same instant.
func stateExpiry(prev types.String, reported *time.Time) types.String {
	if reported == nil {
		return types.StringNull()
	}
	if !prev.IsNull() && !prev.IsUnknown() {
		if t, err := time.Parse(time.RFC3339, prev.ValueString()); err == nil && t.Equal(*reported) {
			return prev
		}
	}
	return types.StringValue(reported.UTC().Format(time.RFC3339))
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "permission", "repositories", "expires_at":
		return path.Root(field), true
	default:
		return path.Path{}, false
	}
}

// Hints for the refusals a user can act on.
var (
	createHints = map[string]string{
		"REGISTRY_TOKEN_LIMIT_REACHED":     "Your organization already has the most registry tokens it may have (50). Revoke one you no longer use.",
		client.CodeRegistryNamespaceNotSet: "Claim a namespace first with pantechdynamics_container_registry_namespace.",
		client.CodeRepositoryNotFound:      "A repository in repositories does not exist. Create it first, and make this resource depend on it.",
		"FORBIDDEN":                        "Only owners and admins can manage registry tokens. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":               "Creating a registry token needs an API key with the write scope.",
		client.CodeRegistryUnavailable:     "The container registry is not available on this platform yet.",
	}
	deleteHints = map[string]string{
		"INSUFFICIENT_DELETE_SCOPE":    "Revoking a registry token needs an API key with the delete scope.",
		"INSUFFICIENT_SCOPE":           "Revoking a registry token needs an API key with the delete scope.",
		"FORBIDDEN":                    "Only owners and admins can manage registry tokens. Use an API key created by one.",
		client.CodeRegistryUnavailable: "The container registry is not available on this platform yet.",
	}
)

// repositoryNames checks each element against the repository grammar.
type repositoryNames struct{}

func (repositoryNames) Description(context.Context) string {
	return "each must be a repository name such as api/server"
}

func (v repositoryNames) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (repositoryNames) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, e := range req.ConfigValue.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() || s.IsNull() {
			continue
		}
		if !resourcekit.ValidRepositoryName(s.ValueString()) {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid repository name", fmt.Sprintf("%q is not a repository name such as \"api/server\".", s.ValueString()))
		}
	}
}

// rfc3339 checks a timestamp.
type rfc3339 struct{}

func (rfc3339) Description(context.Context) string {
	return "must be an RFC 3339 time such as 2027-11-10T00:00:00Z"
}

func (v rfc3339) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (rfc3339) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := time.Parse(time.RFC3339, req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid expiry", fmt.Sprintf("Expected an RFC 3339 time such as \"2027-11-10T00:00:00Z\", got %q.", req.ConfigValue.ValueString()))
	}
}
