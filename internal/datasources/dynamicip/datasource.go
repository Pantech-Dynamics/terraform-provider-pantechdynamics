// Package dynamicip implements the pantechdynamics_dynamic_ip data source.
package dynamicip

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// dynamicIPAPI is the part of the API client this data source needs.
type dynamicIPAPI interface {
	GetDynamicIP(ctx context.Context, instanceID string) (*client.DynamicIP, error)
}

var _ datasource.DataSourceWithConfigure = &DataSource{}

// DataSource reads the Dynamic IP configuration, current address, and recent
// rotation events of an instance.
type DataSource struct {
	api dynamicIPAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID               types.String  `tfsdk:"id"`
	InstanceID       types.String  `tfsdk:"instance_id"`
	Schedule         types.String  `tfsdk:"schedule"`
	Connections      types.String  `tfsdk:"connections"`
	OnDemand         types.Bool    `tfsdk:"on_demand"`
	State            types.String  `tfsdk:"state"`
	CurrentAddress   types.String  `tfsdk:"current_address"`
	NextRotationAt   types.String  `tfsdk:"next_rotation_at"`
	LastRotatedAt    types.String  `tfsdk:"last_rotated_at"`
	SurchargePercent types.Float64 `tfsdk:"surcharge_percent"`
	OnDemandUsed     types.Int64   `tfsdk:"on_demand_used"`
	OnDemandLimit    types.Int64   `tfsdk:"on_demand_limit"`
	FailureReason    types.String  `tfsdk:"failure_reason"`
	Events           []eventModel  `tfsdk:"events"`
}

type eventModel struct {
	Kind        types.String `tfsdk:"kind"`
	FromAddress types.String `tfsdk:"from_address"`
	ToAddress   types.String `tfsdk:"to_address"`
	Degraded    types.Bool   `tfsdk:"degraded"`
	OccurredAt  types.String `tfsdk:"occurred_at"`
}

// Metadata sets the type name: pantechdynamics_dynamic_ip.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dynamic_ip"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the Dynamic IP configuration, current rotating outbound public address, and recent rotation events of an instance.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Identifier of the Dynamic IP configuration, equal to instance_id.",
				Computed:    true,
			},
			"instance_id": schema.StringAttribute{
				Description: "Id of the instance, starting with vm_.",
				Required:    true,
				Validators:  []validator.String{resourcekit.IDPrefix("instance", "vm_")},
			},
			"schedule": schema.StringAttribute{
				Description: "How often the outbound public address rotates: \"every_12_hours\", \"daily\", \"weekly\", or \"monthly\".",
				Computed:    true,
			},
			"connections": schema.StringAttribute{
				Description: "How open outbound connections behave on rotation: \"keep\" or \"move\".",
				Computed:    true,
			},
			"on_demand": schema.BoolAttribute{
				Description: "Whether on-demand rotation is allowed for this instance.",
				Computed:    true,
			},
			"state": schema.StringAttribute{
				Description: "Current lifecycle state of Dynamic IP: \"enabling\", \"active\", \"degraded\", \"disabling\", \"off\", or \"failed\".",
				Computed:    true,
			},
			"current_address": schema.StringAttribute{
				Description: "The rotating public IPv4 address currently used for outbound traffic, or null before it is ready or while falling back to the instance's own address.",
				Computed:    true,
			},
			"next_rotation_at": schema.StringAttribute{
				Description: "When the next scheduled rotation is due, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"last_rotated_at": schema.StringAttribute{
				Description: "When the outbound address last rotated, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"surcharge_percent": schema.Float64Attribute{
				Description: "Percentage added to the instance's hourly price while Dynamic IP is enabled.",
				Computed:    true,
			},
			"on_demand_used": schema.Int64Attribute{
				Description: "Number of on-demand rotations performed in the last 24 hours.",
				Computed:    true,
			},
			"on_demand_limit": schema.Int64Attribute{
				Description: "Maximum number of on-demand rotations allowed per 24 hours.",
				Computed:    true,
			},
			"failure_reason": schema.StringAttribute{
				Description: "Explanation when state is \"failed\" or \"degraded\", or null otherwise.",
				Computed:    true,
			},
			"events": schema.ListNestedAttribute{
				Description: "Recent Dynamic IP rotation events, newest first.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"kind": schema.StringAttribute{
							Description: "Event type: \"enabled\", \"scheduled\", \"on_demand\", \"failover\", \"handover\", or \"disabled\".",
							Computed:    true,
						},
						"from_address": schema.StringAttribute{
							Description: "Previous rotating public IPv4 address, or null.",
							Computed:    true,
						},
						"to_address": schema.StringAttribute{
							Description: "New rotating public IPv4 address, or null.",
							Computed:    true,
						},
						"degraded": schema.BoolAttribute{
							Description: "Whether Dynamic IP was degraded when this event occurred.",
							Computed:    true,
						},
						"occurred_at": schema.StringAttribute{
							Description: "When the event occurred, in RFC 3339 UTC, to the second.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

// Configure receives the API client from the provider.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(dynamicIPAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// Read fetches the instance's Dynamic IP configuration and recent events.
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := d.api.GetDynamicIP(ctx, cfg.InstanceID.ValueString())
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading Dynamic IP", err, readHints)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, toModel(got))...)
}

func toModel(dip *client.DynamicIP) model {
	events := make([]eventModel, 0, len(dip.Events))
	for _, ev := range dip.Events {
		events = append(events, eventModel{
			Kind:        types.StringValue(ev.Kind),
			FromAddress: resourcekit.OptionalString(ev.FromAddress),
			ToAddress:   resourcekit.OptionalString(ev.ToAddress),
			Degraded:    types.BoolValue(ev.Degraded),
			OccurredAt:  resourcekit.Timestamp(ev.OccurredAt),
		})
	}
	return model{
		ID:               types.StringValue(dip.InstanceID),
		InstanceID:       types.StringValue(dip.InstanceID),
		Schedule:         types.StringValue(dip.Schedule),
		Connections:      types.StringValue(dip.Connections),
		OnDemand:         types.BoolValue(dip.OnDemand),
		State:            types.StringValue(dip.State),
		CurrentAddress:   resourcekit.OptionalString(dip.CurrentAddress),
		NextRotationAt:   resourcekit.Timestamp(dip.NextRotationAt),
		LastRotatedAt:    resourcekit.Timestamp(dip.LastRotatedAt),
		SurchargePercent: types.Float64Value(dip.SurchargePercent),
		OnDemandUsed:     types.Int64Value(dip.OnDemandUsed),
		OnDemandLimit:    types.Int64Value(dip.OnDemandLimit),
		FailureReason:    resourcekit.OptionalString(dip.FailureReason),
		Events:           events,
	}
}

var readHints = map[string]string{
	client.CodeAddressRotationNotFound: "Dynamic IP is not enabled on this instance, or the instance does not exist.",
	"FORBIDDEN":                        "Your role is not permitted to read Dynamic IP.",
}
