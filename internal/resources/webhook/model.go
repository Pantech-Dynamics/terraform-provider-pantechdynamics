package webhook

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go. The tfsdk tags tie each field to an
// attribute name in the schema. Events is a types.List, not []string, because a
// list can be unknown in a plan (for example when built from another resource).
type model struct {
	ID             types.String `tfsdk:"id"`
	URL            types.String `tfsdk:"url"`
	Description    types.String `tfsdk:"description"`
	Events         types.List   `tfsdk:"events"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	DisabledReason types.String `tfsdk:"disabled_reason"`
	FailingSince   types.String `tfsdk:"failing_since"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
	Secret         types.String `tfsdk:"secret"`
}

// eventsOf reads the configured event list in order.
func eventsOf(ctx context.Context, m model) ([]string, diag.Diagnostics) {
	var events []string
	diags := m.Events.ElementsAs(ctx, &events, false)
	return events, diags
}

// toCreateRequest turns the plan into the create request. An empty description
// is left out, which the backend reads as no description.
func toCreateRequest(ctx context.Context, plan model) (client.CreateWebhookRequest, diag.Diagnostics) {
	events, diags := eventsOf(ctx, plan)
	return client.CreateWebhookRequest{
		URL:         plan.URL.ValueString(),
		Description: plan.Description.ValueString(),
		Events:      events,
	}, diags
}

// toUpdateRequest sends only what changed between state and plan. Sending an
// unchanged enabled = true is avoided on purpose: enabling a webhook clears its
// failure history, which a change to the URL alone should not do.
func toUpdateRequest(ctx context.Context, state, plan model) (client.UpdateWebhookRequest, diag.Diagnostics) {
	var req client.UpdateWebhookRequest
	var diags diag.Diagnostics

	if !plan.URL.Equal(state.URL) {
		v := plan.URL.ValueString()
		req.URL = &v
	}
	if !plan.Description.Equal(state.Description) {
		v := plan.Description.ValueString()
		req.Description = &v
	}
	if !plan.Events.Equal(state.Events) {
		events, d := eventsOf(ctx, plan)
		diags.Append(d...)
		req.Events = &events
	}
	if !plan.Enabled.Equal(state.Enabled) {
		v := plan.Enabled.ValueBool()
		req.Enabled = &v
	}
	return req, diags
}

// fromAPIResponse builds the state from the API webhook. secret is never
// returned after creation, so it is carried over from the plan or prior state
// (null after an import).
func fromAPIResponse(ctx context.Context, w *client.Webhook, secret types.String) (model, diag.Diagnostics) {
	events := w.Events
	if events == nil {
		events = []string{}
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, events)

	if secret.IsUnknown() {
		secret = types.StringNull()
	}
	return model{
		ID:             types.StringValue(w.ID),
		URL:            types.StringValue(w.URL),
		Description:    types.StringValue(w.Description),
		Events:         list,
		Enabled:        types.BoolValue(w.Enabled),
		DisabledReason: resourcekit.OptionalString(w.DisabledReason),
		FailingSince:   resourcekit.Timestamp(w.FailingSince),
		CreatedAt:      resourcekit.Timestamp(w.CreatedAt),
		UpdatedAt:      resourcekit.Timestamp(w.UpdatedAt),
		Secret:         secret,
	}, diags
}
