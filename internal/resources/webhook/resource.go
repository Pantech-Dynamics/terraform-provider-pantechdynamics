// Package webhook implements the pantechdynamics_webhook resource.
package webhook

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// idPrefix is the prefix every webhook id carries.
const idPrefix = "whk_"

// webhookAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type webhookAPI interface {
	CreateWebhook(ctx context.Context, req client.CreateWebhookRequest) (*client.WebhookWithSecret, error)
	GetWebhook(ctx context.Context, id string) (*client.Webhook, error)
	UpdateWebhook(ctx context.Context, id string, req client.UpdateWebhookRequest) (*client.Webhook, error)
	DeleteWebhook(ctx context.Context, id string) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one webhook: a URL the platform sends the organization's
// events to.
type Resource struct {
	api webhookAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_webhook.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A webhook: an https URL Pantech sends your organization's events to, as signed JSON POST requests. Every argument changes in place. " +
			"The signing secret is shown once, when the webhook is created, and is kept in state as secret. Rotating the secret is done in the console or the CLI, not by Terraform; after a rotation, the secret in state is stale. " +
			"At most 10 webhooks per organization, and only an API key made by an owner or admin can manage them. Webhooks are free.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the webhook, starting with whk_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"url": schema.StringAttribute{
				Description: "Where events are sent: an https URL on the public internet, at most 2,048 characters, with no user name or password.",
				Required:    true,
				Validators:  []validator.String{httpsURL{}},
			},
			"events": schema.ListAttribute{
				Description: "What the webhook subscribes to, 1 to 100 entries, each listed once: an event type such as \"instance.created\" (see the pantechdynamics_webhook_event_types data source), \"<resource>.*\" for every event of one resource (for example \"instance.*\"), or \"*\" for everything. The order is kept.",
				Required:    true,
				ElementType: types.StringType,
				Validators:  []validator.List{eventList{}},
			},
			"description": schema.StringAttribute{
				Description: "A note for yourself, at most 255 characters. Defaults to \"\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Validators:  []validator.String{maxLength{max: maxDescriptionLength}},
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether events are sent to the webhook. Defaults to true. The platform disables a webhook whose deliveries have failed for three days (disabled_reason = \"failing\"); the next apply with enabled = true enables it again, which clears its failure history.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"disabled_reason": schema.StringAttribute{
				Description: "Why the webhook is disabled: \"disabled_by_user\", or \"failing\" after three days of failed deliveries. Null while it is enabled.",
				Computed:    true,
			},
			"failing_since": schema.StringAttribute{
				Description: "When deliveries started failing with no success since, in RFC 3339 UTC, to the second. Null while they succeed.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the webhook was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the webhook last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"secret": schema.StringAttribute{
				Description: "The signing secret (whsec_...), used to verify the Pantech-Signature header of each delivery. The platform shows it only in the create response, so it exists only in Terraform state: treat the state as a secret. " +
					"It is null after an import, and it does not follow a rotation made in the console or CLI.",
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
	api, ok := req.ProviderData.(webhookAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create adds the webhook and saves it, with its one-time secret, straight away.
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

	created, err := r.api.CreateWebhook(ctx, body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating webhook", err, attributeFor, createHints)
		return
	}

	secret := types.StringPointerValue(created.Secret)
	if created.Secret == nil {
		// A retried create returns the webhook without its secret, which only
		// the lost first response carried.
		resp.Diagnostics.AddWarning("Webhook secret not returned",
			"The webhook "+created.ID+" was created, but the response that carried its signing secret was lost, so secret is empty in state. "+
				"Rotate the secret in the console or the CLI to get a new one.")
	}
	r.saveState(ctx, &created.Webhook, secret, &resp.State, &resp.Diagnostics)
}

// Read refreshes state from the API. A webhook deleted outside Terraform is
// removed from state so the next plan creates it again.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hook, err := r.api.GetWebhook(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading webhook", err, nil)
		return
	}
	r.saveState(ctx, hook, state.Secret, &resp.State, &resp.Diagnostics)
}

// Update changes the arguments that differ from state, in place.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := toUpdateRequest(ctx, state, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	hook, err := r.api.UpdateWebhook(ctx, state.ID.ValueString(), body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating webhook", err, attributeFor, updateHints)
		return
	}
	r.saveState(ctx, hook, state.Secret, &resp.State, &resp.Diagnostics)
}

// Delete removes the webhook. One that is already gone counts as success: a
// repeated delete returns 404 and the goal state is reached.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.api.DeleteWebhook(ctx, state.ID.ValueString())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error deleting webhook", err, nil)
	}
}

// ImportState adopts an existing webhook by id. Its secret cannot be read back,
// so it stays null.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, "webhook", idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// saveState writes the webhook to state, keeping the given secret.
func (r *Resource) saveState(ctx context.Context, hook *client.Webhook, secret types.String, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	m, d := fromAPIResponse(ctx, hook, secret)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	diags.Append(state.Set(ctx, m)...)
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "url", "events", "description", "enabled":
		return path.Root(field), true
	default:
		return path.Path{}, false
	}
}

// Hints for the refusals a user can act on.
var (
	createHints = map[string]string{
		client.CodeWebhookLimitReached: "Your organization already has the most webhooks it may have (10). Delete one, or subscribe an existing webhook to more events.",
		"FORBIDDEN":                    "Only owners and admins can manage webhooks. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":           "Managing webhooks needs an API key with the write scope.",
		client.CodeWebhooksUnavailable: "Webhooks are not available on this platform yet. Try again later.",
	}
	updateHints = map[string]string{
		"CONFLICT":                     "The webhook was changed at the same time by someone else. Run terraform apply again.",
		"FORBIDDEN":                    "Only owners and admins can manage webhooks. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":           "Managing webhooks needs an API key with the write scope.",
		client.CodeWebhooksUnavailable: "Webhooks are not available on this platform yet. Try again later.",
	}
)
