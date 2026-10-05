// Package databaseengines implements the pantechdynamics_database_engines data
// source.
package databaseengines

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// engineAPI is the part of the API client this data source needs.
type engineAPI interface {
	ListDatabaseEngines(ctx context.Context) ([]client.DatabaseEngine, error)
}

var _ datasource.DataSourceWithConfigure = &DataSource{}

// DataSource lists the engines and version lines offered for new databases.
type DataSource struct {
	api engineAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID      types.String  `tfsdk:"id"`
	Engines []engineModel `tfsdk:"engines"`
}

type engineModel struct {
	Engine      types.String   `tfsdk:"engine"`
	DisplayName types.String   `tfsdk:"display_name"`
	Port        types.Int64    `tfsdk:"port"`
	Versions    []versionModel `tfsdk:"versions"`
}

type versionModel struct {
	Version types.String `tfsdk:"version"`
	EOLDate types.String `tfsdk:"eol_date"`
	Zones   []string     `tfsdk:"zones"`
}

// Metadata sets the type name: pantechdynamics_database_engines.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_engines"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the database engines and the long-term-support version lines offered for new pantechdynamics_database resources, with the zones each version can be created in. A version appears only while it is offered.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Always \"database_engines\"."},
			"engines": schema.ListNestedAttribute{
				Description: "The engines.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"engine":       schema.StringAttribute{Computed: true, Description: "The value the database's engine argument takes, for example \"postgresql\"."},
					"display_name": schema.StringAttribute{Computed: true, Description: "Name for display, for example \"PostgreSQL\"."},
					"port":         schema.Int64Attribute{Computed: true, Description: "TCP port the engine listens on."},
					"versions": schema.ListNestedAttribute{
						Description: "Version lines offered. Empty when the engine has none on offer.",
						Computed:    true,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"version":  schema.StringAttribute{Computed: true, Description: "The value the database's version argument takes, for example \"18\"."},
							"eol_date": schema.StringAttribute{Computed: true, Description: "End-of-life date of the line (YYYY-MM-DD), or null when not announced."},
							"zones":    schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Zones this version can be created in."},
						}},
					},
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
	api, ok := req.ProviderData.(engineAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.api = api
}

// Read fetches the engines.
func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	engines, err := d.api.ListDatabaseEngines(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading database engines", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(engines))...)
}

// fromAPIResponse builds the state. Lists are never null, so an engine with no
// version on offer shows an empty list.
func fromAPIResponse(engines []client.DatabaseEngine) model {
	m := model{ID: types.StringValue("database_engines"), Engines: make([]engineModel, 0, len(engines))}
	for _, e := range engines {
		em := engineModel{
			Engine: types.StringValue(e.Engine), DisplayName: types.StringValue(e.DisplayName),
			Port: types.Int64Value(e.Port), Versions: make([]versionModel, 0, len(e.Versions)),
		}
		for _, v := range e.Versions {
			zones := v.Zones
			if zones == nil {
				zones = []string{}
			}
			em.Versions = append(em.Versions, versionModel{
				Version: types.StringValue(v.Version), EOLDate: types.StringPointerValue(v.EOLDate), Zones: zones,
			})
		}
		m.Engines = append(m.Engines, em)
	}
	return m
}
