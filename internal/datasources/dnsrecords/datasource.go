// Package dnsrecords implements the pantechdynamics_dns_records data source.
package dnsrecords

import (
	"context"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// recordsAPI is the part of the API client this data source needs.
type recordsAPI interface {
	ListDNSRecords(ctx context.Context, zoneID string, filter client.DNSRecordFilter) ([]client.DNSRecord, error)
}

var _ datasource.DataSourceWithConfigure = &DataSource{}

// DataSource lists the record sets of one zone.
type DataSource struct {
	api recordsAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID      types.String  `tfsdk:"id"`
	ZoneID  types.String  `tfsdk:"zone_id"`
	Type    types.String  `tfsdk:"type"`
	Name    types.String  `tfsdk:"name"`
	Records []recordModel `tfsdk:"records"`
}

type recordModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	FQDN         types.String `tfsdk:"fqdn"`
	Type         types.String `tfsdk:"type"`
	TTL          types.Int64  `tfsdk:"ttl"`
	Values       types.List   `tfsdk:"values"`
	AllowPrivate types.Bool   `tfsdk:"allow_private"`
	Comment      types.String `tfsdk:"comment"`
	Managed      types.Bool   `tfsdk:"managed"`
	Status       types.String `tfsdk:"status"`
	Version      types.Int64  `tfsdk:"version"`
}

// Metadata sets the type name: pantechdynamics_dns_records.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_records"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the record sets of a Pantech DNS zone, by name then type, including the apex SOA and NS records Pantech DNS manages (managed = true). Narrow the list with type or name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "The zone id, followed by the filters used."},
			"zone_id": schema.StringAttribute{
				Required: true, Description: "Id of the zone (dnsz_...).",
				Validators: []validator.String{resourcekit.IDPrefix("dns zone", "dnsz_")},
			},
			"type": schema.StringAttribute{
				Optional: true, Description: "Only record sets of this type: " + strings.Join(dnsname.RecordTypes, ", ") + ", or SOA.",
				Validators: []validator.String{resourcekit.OneOf(slices.Concat(dnsname.RecordTypes, []string{"SOA"})...)},
			},
			"name": schema.StringAttribute{Optional: true, Description: "Only record sets with this name relative to the zone, such as \"www\" or \"@\"."},
			"records": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The record sets, in the order the API lists them.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":            schema.StringAttribute{Computed: true, Description: "Id of the record set (dnsr_...)."},
					"name":          schema.StringAttribute{Computed: true, Description: "Name relative to the zone; \"@\" is the apex."},
					"fqdn":          schema.StringAttribute{Computed: true, Description: "The fully-qualified name, without a trailing dot."},
					"type":          schema.StringAttribute{Computed: true, Description: "Record type."},
					"ttl":           schema.Int64Attribute{Computed: true, Description: "Time to live, in seconds."},
					"values":        schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "The values, in the canonical form the DNS server stores (host names end in a dot)."},
					"allow_private": schema.BoolAttribute{Computed: true, Description: "Whether private addresses are allowed."},
					"comment":       schema.StringAttribute{Computed: true, Description: "The note on the record set."},
					"managed":       schema.BoolAttribute{Computed: true, Description: "True for the apex SOA and NS records, which Pantech DNS manages and which cannot be changed."},
					"status":        schema.StringAttribute{Computed: true, Description: "\"pending\", \"active\", \"deleting\" or \"failed\"."},
					"version":       schema.Int64Attribute{Computed: true, Description: "Version of the record set."},
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
	api, ok := req.ProviderData.(recordsAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// Read lists the zone's record sets, filtered as asked. The name filter is
// matched here as well, against the relative name, so "WWW" and "www" both
// work whatever form the backend's filter takes.
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zoneID := cfg.ZoneID.ValueString()
	filter := client.DNSRecordFilter{Type: cfg.Type.ValueString()}
	records, err := d.api.ListDNSRecords(ctx, zoneID, filter)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading DNS records", err, map[string]string{
			client.CodeDNSZoneNotFound: "The zone does not exist. Check zone_id.",
			client.CodeDNSUnavailable:  "Pantech DNS is not available on this platform yet.",
		})
		return
	}
	m, diags := fromAPIResponse(ctx, cfg, records)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// fromAPIResponse builds the state. The list is never null, so a zone with no
// match shows an empty list.
func fromAPIResponse(ctx context.Context, cfg model, records []client.DNSRecord) (model, diag.Diagnostics) {
	var diags diag.Diagnostics
	want := strings.ToLower(strings.TrimSpace(cfg.Name.ValueString()))
	m := model{
		ID:      types.StringValue(strings.Join([]string{cfg.ZoneID.ValueString(), cfg.Type.ValueString(), cfg.Name.ValueString()}, "/")),
		ZoneID:  cfg.ZoneID,
		Type:    cfg.Type,
		Name:    cfg.Name,
		Records: make([]recordModel, 0, len(records)),
	}
	for _, r := range records {
		if want != "" && r.Name != want && r.FQDN != strings.TrimSuffix(want, ".") {
			continue
		}
		values := r.Values
		if values == nil {
			values = []string{}
		}
		list, d := types.ListValueFrom(ctx, types.StringType, values)
		diags.Append(d...)
		m.Records = append(m.Records, recordModel{
			ID: types.StringValue(r.ID), Name: types.StringValue(r.Name), FQDN: types.StringValue(r.FQDN), Type: types.StringValue(r.Type),
			TTL: types.Int64Value(r.TTL), Values: list, AllowPrivate: types.BoolValue(r.AllowPrivate), Comment: types.StringValue(r.Comment),
			Managed: types.BoolValue(r.Managed), Status: types.StringValue(r.Status), Version: types.Int64Value(r.Version),
		})
	}
	return m, diags
}
