// Package sshkey implements the pantechdynamics_ssh_key resource.
package sshkey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// idPrefix is the prefix every ssh key id carries.
const idPrefix = "sshk_"

// keyAPI is the part of the API client this resource needs. It is defined here,
// by the consumer, so tests can fake it. Go interfaces are satisfied implicitly:
// *client.Client matches without declaring "implements".
type keyAPI interface {
	CreateSSHKey(ctx context.Context, req client.CreateSSHKeyRequest) (*client.SSHKey, error)
	GetSSHKey(ctx context.Context, id string) (*client.SSHKey, error)
	ListSSHKeys(ctx context.Context) ([]client.SSHKey, error)
	DeleteSSHKey(ctx context.Context, id string) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one SSH public key on the account.
type Resource struct {
	api keyAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_ssh_key.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An SSH public key that can be attached to instances. SSH keys cannot be edited: changing the name or public key replaces the key.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the key, starting with sshk_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Name of the key. Must be unique on the account. Changing it replaces the key.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"public_key": schema.StringAttribute{
				Description: "The public key in OpenSSH format. If omitted, a keypair is generated and the private key is available once, as private_key. Changing it replaces the key.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"fingerprint": schema.StringAttribute{
				Description:   "SHA256 fingerprint of the public key.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Description:   "When the key was registered, in RFC 3339 UTC.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"private_key": schema.StringAttribute{
				Description:   "The generated private key. Set only when public_key was omitted. The backend shows it once, so it exists only in Terraform state. Treat the state as a secret.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// Configure receives the API client built by the provider. Terraform calls it
// early with no data, so a nil ProviderData is normal and ignored.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(keyAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	r.api = api
}

// Create registers the key and saves it to state straight away, including the
// one-time private key.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := createKey(ctx, r.api, toCreateRequest(plan))
	if err != nil {
		addCreateError(&resp.Diagnostics, err)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, key))...)
}

// Read refreshes state from the API. A key deleted outside Terraform is removed
// from state so the next plan recreates it.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.api.GetSSHKey(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Error reading SSH key", err)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, key))...)
}

// Update is never reached in practice: every settable attribute forces a
// replacement. It fails loudly rather than pretending to update.
func (r *Resource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("SSH keys cannot be updated", "The API has no update endpoint. This is a bug in the provider: a change should have replaced the key.")
}

// Delete removes the key. A key that is already gone counts as success, because
// a repeated delete returns 404 and the goal state is reached.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.api.DeleteSSHKey(ctx, state.ID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		addAPIError(&resp.Diagnostics, "Error deleting SSH key", err)
	}
}

// ImportState adopts an existing key by id. A wrong id fails here with a clear
// message instead of a 404 after the fact.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !strings.HasPrefix(req.ID, idPrefix) {
		resp.Diagnostics.AddError("Invalid SSH key id", fmt.Sprintf("Expected an id starting with %q, got %q.", idPrefix, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
