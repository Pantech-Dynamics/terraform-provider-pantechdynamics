// Package objectstoragekey implements the pantechdynamics_object_storage_key
// resource: an S3 access key for the organization's buckets (closed beta).
package objectstoragekey

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "osk_"
	kind     = "object storage key"
	maxName  = 64
)

// keyAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type keyAPI interface {
	CreateObjectStorageKey(ctx context.Context, req client.CreateObjectStorageKeyRequest) (*client.ObjectStorageKeyWithSecret, error)
	ListObjectStorageKeys(ctx context.Context) ([]client.ObjectStorageKey, error)
	DeleteObjectStorageKey(ctx context.Context, id string) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one access key. Keys cannot change, so every argument
// replaces it.
type Resource struct {
	api keyAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// model maps the resource's attributes to Go.
type model struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Region          types.String `tfsdk:"region"`
	ReadOnly        types.Bool   `tfsdk:"read_only"`
	Buckets         types.Set    `tfsdk:"buckets"`
	AccessKeyID     types.String `tfsdk:"access_key_id"`
	SecretAccessKey types.String `tfsdk:"secret_access_key"`
	Endpoint        types.String `tfsdk:"endpoint"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_object_storage_key.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_object_storage_key"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An S3 access key for the organization's buckets (object storage, closed beta). Use access_key_id and secret_access_key with any S3 tool at endpoint. " +
			"The secret is returned only when the key is created, so it lives in Terraform state: keep state somewhere safe. Keys cannot change; every argument replaces the key.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the key, starting with osk_.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"name": schema.StringAttribute{
				Description:   "A name such as \"app\", unique in the organization, at most 64 characters. Changing it replaces the key.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.NameLength(kind, maxName)},
			},
			"region": schema.StringAttribute{
				Description:   "Region code, such as \"af-abj\". Leave it out for the default region. Changing it replaces the key.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"read_only": schema.BoolAttribute{
				Description:   "The key may read but not write or delete. Defaults to false. Changing it replaces the key.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"buckets": schema.SetAttribute{
				Description:   "Bucket names the key may use. Leave it out for every bucket in the organization. Changing it replaces the key.",
				Optional:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			},
			"access_key_id": schema.StringAttribute{
				Description:   "The S3 access key id.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"secret_access_key": schema.StringAttribute{
				Description:   "The S3 secret access key. The API shows it only in the create response, so it exists only in Terraform state. It is null after an import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"endpoint": schema.StringAttribute{
				Description:   "The S3 endpoint URL for the key's region, from the create response. Null after an import.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the key was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: keep,
			},
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(keyAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create issues the key and saves it, with its one-time secret, straight away.
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
	created, err := r.api.CreateObjectStorageKey(ctx, body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating object storage key", err, attributeFor, hints)
		return
	}
	m := fromAPIResponse(ctx, plan, &created.ObjectStorageKey, &resp.Diagnostics)
	m.SecretAccessKey = optional(created.SecretAccessKey)
	m.Endpoint = optional(created.Endpoint)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Read refreshes state from the key list (there is no single read). A key
// revoked outside Terraform is removed from state, so the next plan issues a
// new one.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key, err := r.find(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading object storage key", err, hints)
		return
	}
	m := fromAPIResponse(ctx, state, key, &resp.Diagnostics)
	m.SecretAccessKey, m.Endpoint = nullIfUnknown(state.SecretAccessKey), nullIfUnknown(state.Endpoint)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Update is never asked to change anything: every argument replaces the key.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete revokes the key. One already gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.api.DeleteObjectStorageKey(ctx, state.ID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error revoking object storage key", err, hints)
	}
}

// ImportState adopts a key by id. Its secret cannot be read back, so it stays
// null; the key keeps working for whoever holds the secret.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.AddWarning("Object storage key secret not imported",
		"The API never returns a key's secret after it is created, so secret_access_key is null in state. Replace the key to have Terraform hold a secret.")
}

// find returns the key from the list, or ErrNotFound.
func (r *Resource) find(ctx context.Context, id string) (*client.ObjectStorageKey, error) {
	keys, err := r.api.ListObjectStorageKeys(ctx)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		if keys[i].ID == id {
			return &keys[i], nil
		}
	}
	return nil, fmt.Errorf("object storage key %s: %w", id, client.ErrNotFound)
}

// toCreateRequest turns the plan into the create request. An unset buckets
// set means every bucket.
func toCreateRequest(ctx context.Context, plan model) (client.CreateObjectStorageKeyRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	req := client.CreateObjectStorageKeyRequest{Name: plan.Name.ValueString(), ReadOnly: plan.ReadOnly.ValueBool()}
	if !plan.Region.IsNull() && !plan.Region.IsUnknown() {
		req.Region = plan.Region.ValueString()
	}
	if !plan.Buckets.IsNull() && !plan.Buckets.IsUnknown() {
		diags.Append(plan.Buckets.ElementsAs(ctx, &req.Buckets, false)...)
		slices.Sort(req.Buckets)
	}
	return req, diags
}

// fromAPIResponse builds the state from a key. The secret and endpoint are
// set by the caller: only the create response carries them.
func fromAPIResponse(ctx context.Context, prev model, k *client.ObjectStorageKey, diags *diag.Diagnostics) model {
	return model{
		ID:          types.StringValue(k.ID),
		Name:        types.StringValue(k.Name),
		Region:      types.StringValue(k.Region),
		ReadOnly:    types.BoolValue(k.ReadOnly),
		Buckets:     stateBuckets(ctx, prev.Buckets, k.Buckets, diags),
		AccessKeyID: types.StringValue(k.AccessKeyID),
		CreatedAt:   resourcekit.Timestamp(k.CreatedAt),
	}
}

// stateBuckets keeps the configured set (null or empty, for every bucket)
// when it is the same as what the API reports.
func stateBuckets(ctx context.Context, prev types.Set, reported []string, diags *diag.Diagnostics) types.Set {
	if !prev.IsUnknown() {
		var configured []string
		if !prev.IsNull() {
			diags.Append(prev.ElementsAs(ctx, &configured, false)...)
		}
		if sameSet(configured, reported) {
			return prev
		}
	}
	if len(reported) == 0 {
		return types.SetNull(types.StringType)
	}
	set, d := types.SetValueFrom(ctx, types.StringType, reported)
	diags.Append(d...)
	return set
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func optional(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func nullIfUnknown(s types.String) types.String {
	if s.IsUnknown() {
		return types.StringNull()
	}
	return s
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "region", "read_only", "buckets":
		return path.Root(field), true
	}
	return path.Path{}, false
}

// hints explain the refusals a user can act on.
var hints = map[string]string{
	client.CodeObjectStorageNotEnabled: "Object storage is in closed beta and is not switched on for your organization. Ask support to join the beta.",
	"OBJECT_STORAGE_KEY_NAME_TAKEN":    "The organization already has a key with this name. Choose another name.",
	"OBJECT_STORAGE_KEY_EXISTS":        "The organization already has a key with this name. Choose another name.",
	"OBJECT_STORAGE_KEY_LIMIT_REACHED": "Your organization already has the most access keys it may have. Revoke one you no longer use.",
	"OBJECT_STORAGE_UNAVAILABLE":       "Object storage is not answering right now. Try again shortly.",
	"FORBIDDEN":                        "The API key's role may not manage object storage keys. Use a key created by an owner or admin.",
	"INSUFFICIENT_SCOPE":               "Managing object storage keys needs an API key with the write scope.",
	"INSUFFICIENT_DELETE_SCOPE":        "Revoking an object storage key needs an API key with the delete scope.",
}
