// Package cloudconnection implements the pantechdynamics_cloud_connection
// resource: a read-only connection Pantech Migrate uses to scan an account in
// another cloud. Scans, plans and moves are one-shot jobs and stay out of
// Terraform; the connection is the declarative part.
package cloudconnection

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "ccn_"
	kind     = "cloud connection"
	maxName  = 100
)

// connectionAPI is the part of the API client this resource needs.
type connectionAPI interface {
	CreateCloudConnection(ctx context.Context, req client.CreateCloudConnectionRequest) (*client.CloudConnection, error)
	GetCloudConnection(ctx context.Context, id string) (*client.CloudConnection, error)
	UpdateCloudConnection(ctx context.Context, id string, req client.UpdateCloudConnectionRequest) (*client.CloudConnection, error)
	DeleteCloudConnection(ctx context.Context, id string) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one cloud connection.
type Resource struct {
	api connectionAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// hints explain the refusals a user can act on.
var hints = map[string]string{
	client.CodeMigrateUnavailable: "Pantech Migrate is not available on this platform yet.",
}

// Metadata sets the type name: pantechdynamics_cloud_connection.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_connection"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Description: desc, Computed: true, PlanModifiers: keep}
	}
	resp.Schema = schema.Schema{
		Description: "A read-only connection Pantech Migrate uses to scan an account in another cloud and plan its move to Pantech. Nothing is shared when it is created: its setup says what to run in the other cloud. " +
			"AWS, Google Cloud, Azure and Huawei Cloud are federated: run the setup there, then send what it printed with pantechdynamics_cloud_connection_federation. " +
			"DigitalOcean, Hetzner, Linode, Vultr, Contabo and OpenStack are read by the Pantech Migrate agent, which you run with a read-only token that never leaves your machine; enroll it with `pantech migrate agent enroll`. " +
			"Scans, plans and moves are one-shot jobs and are not managed by Terraform. Destroying the connection removes it and its scans; migration plans made from it stay. Owners and admins only; at most 10 connections.",
		Attributes: map[string]schema.Attribute{
			"id": computed("Identifier of the connection, starting with ccn_."),
			"cloud": schema.StringAttribute{
				Description: "The cloud (the API's provider field; provider is a name Terraform reserves): \"aws\" (the default), \"gcp\", \"azure\", \"huawei\", \"digitalocean\", \"hetzner\", \"linode\", \"vultr\", \"contabo\" or \"openstack\". " +
					"The pantechdynamics_cloud_providers data source says which this platform offers. Changing it replaces the connection.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("aws"),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.OneOf(client.CloudProviders...)},
			},
			"name": schema.StringAttribute{
				Description: "A name such as \"Production AWS\", 1 to 100 characters. Changes in place.",
				Required:    true,
				Validators:  []validator.String{resourcekit.NameLength(kind, maxName)},
			},
			"status": schema.StringAttribute{
				Description: "\"awaiting_role\" (federated: waiting for what the setup printed), \"awaiting_agent\" (waiting for the Pantech Migrate agent), \"verifying\", \"connected\" or \"failed\".",
				Computed:    true,
			},
			"failure_code":          schema.StringAttribute{Description: "Why the last check failed, such as \"permission_missing\"; null when it did not.", Computed: true},
			"failure_message":       schema.StringAttribute{Description: "What to do about the failure; null when there is none.", Computed: true},
			"external_account_id":   schema.StringAttribute{Description: "The source account: an AWS account, a Google Cloud pool's project number, an Azure tenant, or what the agent reported. Null until connected.", Computed: true},
			"access":                computed("\"federated\" or \"agent\": how Pantech reads this cloud."),
			"issuer_url":            computed("Pantech's identity provider URL, which the other cloud trusts; null for agent providers."),
			"audience":              computed("The audience every token carries; null for agent providers."),
			"subject":               computed("The subject the other cloud's trust policy requires: this connection's id."),
			"quick_create_url":      computed("AWS only: opens AWS's create-stack page with the template and this connection filled in; null when not offered."),
			"setup_template":        computed("The main setup file: AWS's CloudFormation template (JSON), Google Cloud's gcloud script, Azure's Azure CLI script, Huawei Cloud's steps, or, for agent providers, how to make a read-only token."),
			"setup_template_format": computed("The setup_template's format: \"cloudformation\", \"shell\", \"markdown\" or \"agent\"."),
			"setup_files": schema.MapAttribute{
				Description:   "Further setup files by name, such as a Terraform version of the setup or JSON documents to paste.",
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
			},
			"permissions": schema.ListAttribute{
				Description:   "Every read-only permission the setup grants.",
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"agent_image": computed("Agent providers: the Pantech Migrate agent's container image."),
			"agent_token_variables": schema.ListAttribute{
				Description:   "Agent providers: the environment variables the agent reads the read-only token from, on your machine.",
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"agent_token_how_to":      computed("Agent providers: how to make the read-only token."),
			"agent_enroll_command":    computed("Agent providers: the enrollment command, with <ENROLLMENT_CODE> where the one-time code goes."),
			"agent_run_command":       computed("Agent providers: how to run the agent."),
			"agent_container_command": computed("Agent providers: how to run the agent as a container."),
			"verified_at":             schema.StringAttribute{Description: "When the connection was last verified, in RFC 3339 UTC, to the second.", Computed: true},
			"created_at":              computed("When the connection was created, in RFC 3339 UTC, to the second."),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(connectionAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create starts the connection and saves it with its setup.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	conn, err := r.api.CreateCloudConnection(ctx, client.CreateCloudConnectionRequest{Provider: plan.Cloud.ValueString(), Name: plan.Name.ValueString()})
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating cloud connection", err, attributeFor, hints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ctx, conn, &resp.Diagnostics))...)
}

// Read refreshes the connection. One removed outside Terraform is removed
// from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	conn, err := r.api.GetCloudConnection(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading cloud connection", err, hints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ctx, conn, &resp.Diagnostics))...)
}

// Update renames the connection: name is the only argument that changes in
// place.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	conn, err := r.api.UpdateCloudConnection(ctx, state.ID.ValueString(), client.UpdateCloudConnectionRequest{Name: &name})
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error renaming cloud connection", err, attributeFor, hints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ctx, conn, &resp.Diagnostics))...)
}

// Delete removes the connection and its scans. One already gone counts as
// success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.api.DeleteCloudConnection(ctx, state.ID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting cloud connection", err, hints)
		return
	}
	resp.Diagnostics.AddWarning("Cloud connection removed from Pantech only",
		"Pantech no longer uses this connection. To remove its access in the other cloud too, delete what its setup created there (the AWS stack, the Google Cloud pool, the Azure app registration, the Huawei Cloud identity provider, or the agent's token).")
}

// ImportState adopts a connection by id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// attributeFor maps a 422 field to its attribute.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name":
		return path.Root(field), true
	case "provider":
		return path.Root("cloud"), true
	}
	return path.Empty(), false
}
