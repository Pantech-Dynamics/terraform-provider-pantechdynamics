// Package kubernetesversions implements the pantechdynamics_kubernetes_versions
// data source.
package kubernetesversions

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// versionAPI is the part of the API client this data source needs.
type versionAPI interface {
	ListKubernetesVersions(ctx context.Context, zoneID string) (*client.KubernetesVersionList, error)
}

var _ datasource.DataSourceWithConfigure = &DataSource{}

// DataSource lists the Kubernetes versions offered for new clusters.
type DataSource struct {
	api versionAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID        types.String   `tfsdk:"id"`
	ZoneID    types.String   `tfsdk:"zone_id"`
	Versions  []versionModel `tfsdk:"versions"`
	HAZoneIDs []string       `tfsdk:"ha_zone_ids"`
}

type versionModel struct {
	ID          types.String `tfsdk:"id"`
	ZoneID      types.String `tfsdk:"zone_id"`
	Version     types.String `tfsdk:"version"`
	Status      types.String `tfsdk:"status"`
	MinCPU      types.Int64  `tfsdk:"min_cpu"`
	MinMemoryMB types.Int64  `tfsdk:"min_memory_mb"`
}

// Metadata sets the type name: pantechdynamics_kubernetes_versions.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_versions"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the Kubernetes versions new pantechdynamics_kubernetes_cluster resources can be created with, newest first, each in one zone, and the zones that offer a highly available control plane. A version is withdrawn from new clusters before its upstream end of life; clusters already on it keep running it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "zone_id when it is set, otherwise \"kubernetes_versions\"."},
			"zone_id": schema.StringAttribute{
				Optional:    true,
				Description: "Only list the versions in this zone, for example \"af-abj-2\". Omit it to list every zone's.",
			},
			"versions": schema.ListNestedAttribute{
				Description: "The versions, newest first.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":            schema.StringAttribute{Computed: true, Description: "The value kubernetes_version_id takes, starting with k8sv_."},
					"zone_id":       schema.StringAttribute{Computed: true, Description: "The zone this version can be created in."},
					"version":       schema.StringAttribute{Computed: true, Description: "The Kubernetes version, for example \"1.31.2\"."},
					"status":        schema.StringAttribute{Computed: true, Description: "\"available\", or \"withdrawn\" for a version that takes no new clusters."},
					"min_cpu":       schema.Int64Attribute{Computed: true, Description: "The fewest vCPUs a node_plan needs for this version."},
					"min_memory_mb": schema.Int64Attribute{Computed: true, Description: "The least memory, in MB, a node_plan needs for this version."},
				}},
			},
			"ha_zone_ids": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Zones where a cluster can have 3 control nodes (control_nodes = 3) now.",
			},
		},
	}
}

// Configure receives the API client from the provider.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(versionAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.api = api
}

// Read fetches the versions.
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := d.api.ListKubernetesVersions(ctx, cfg.ZoneID.ValueString())
	if err != nil {
		detail := err.Error()
		if client.HasCode(err, client.CodeKubernetesUnavailable) {
			detail += "\n\nManaged Kubernetes is not open on this platform yet. Contact support to ask when it will be."
		}
		resp.Diagnostics.AddError("Error reading Kubernetes versions", detail)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(cfg.ZoneID, list))...)
}

// fromAPIResponse builds the state. Lists are never null, so a zone with no
// version on offer shows an empty list.
func fromAPIResponse(zoneID types.String, list *client.KubernetesVersionList) model {
	id := "kubernetes_versions"
	if zoneID.ValueString() != "" {
		id = zoneID.ValueString()
	}
	m := model{ID: types.StringValue(id), ZoneID: zoneID, Versions: make([]versionModel, 0, len(list.Data)), HAZoneIDs: list.HAZoneIDs}
	if m.HAZoneIDs == nil {
		m.HAZoneIDs = []string{}
	}
	for _, v := range list.Data {
		m.Versions = append(m.Versions, versionModel{
			ID: types.StringValue(v.ID), ZoneID: types.StringValue(v.ZoneID), Version: types.StringValue(v.Version),
			Status: types.StringValue(v.Status), MinCPU: types.Int64Value(v.MinCPU), MinMemoryMB: types.Int64Value(v.MinMemoryMB),
		})
	}
	return m
}
