// Package webhookeventtypes implements the pantechdynamics_webhook_event_types
// data source.
package webhookeventtypes

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// eventTypeAPI is the part of the API client this data source needs.
type eventTypeAPI interface {
	ListWebhookEventTypes(ctx context.Context) ([]client.WebhookEventType, error)
}

var _ datasource.DataSourceWithConfigure = &DataSource{}

// DataSource lists the event types a webhook can subscribe to.
type DataSource struct {
	api eventTypeAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID         types.String     `tfsdk:"id"`
	EventTypes []eventTypeModel `tfsdk:"event_types"`
}

type eventTypeModel struct {
	Type        types.String `tfsdk:"type"`
	Resource    types.String `tfsdk:"resource"`
	Description types.String `tfsdk:"description"`
}

// Metadata sets the type name: pantechdynamics_webhook_event_types.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook_event_types"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists every event type a pantechdynamics_webhook can subscribe to. Infrastructure events are named <resource>.<past tense> when a change finished (instance.created) and <resource>.<verb>_failed when it failed (instance.create_failed). " +
			"A webhook's events take these types, \"<resource>.*\" for every type of one resource, or \"*\" for all.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Always \"webhook_event_types\"."},
			"event_types": schema.ListNestedAttribute{
				Description: "The event types, in the order the API lists them.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"type":        schema.StringAttribute{Computed: true, Description: "The event type, for example \"instance.created\"."},
					"resource":    schema.StringAttribute{Computed: true, Description: "What the event is about, for example \"instance\". \"<resource>.*\" subscribes to every type of it."},
					"description": schema.StringAttribute{Computed: true, Description: "What the event means."},
				}},
			},
		},
	}
}

// Configure receives the API client from the provider.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(eventTypeAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// Read fetches the event types.
func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	list, err := d.api.ListWebhookEventTypes(ctx)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading webhook event types", err, map[string]string{
			client.CodeWebhooksUnavailable: "Webhooks are not available on this platform yet. Try again later.",
		})
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(list))...)
}

// fromAPIResponse builds the state. The list is never null, so an empty catalog
// shows an empty list.
func fromAPIResponse(list []client.WebhookEventType) model {
	m := model{ID: types.StringValue("webhook_event_types"), EventTypes: make([]eventTypeModel, 0, len(list))}
	for _, e := range list {
		m.EventTypes = append(m.EventTypes, eventTypeModel{
			Type: types.StringValue(e.Type), Resource: types.StringValue(e.Resource), Description: types.StringValue(e.Description),
		})
	}
	return m
}
