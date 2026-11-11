// Package containerrepositories implements the
// pantechdynamics_container_repositories data source (every repository) and
// pantechdynamics_container_repository (one, by id or name). They share their
// attribute descriptions.
package containerrepositories

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// repositoryAPI is the part of the API client these data sources need.
type repositoryAPI interface {
	ListRepositories(ctx context.Context, q string) ([]client.Repository, error)
}

var _ datasource.DataSourceWithConfigure = &DataSource{}

// DataSource lists every repository in the namespace.
type DataSource struct {
	api repositoryAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type listModel struct {
	ID           types.String      `tfsdk:"id"`
	Repositories []repositoryModel `tfsdk:"repositories"`
}

// repositoryModel is one repository. The single lookup embeds the same fields.
type repositoryModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Namespace     types.String `tfsdk:"namespace"`
	FullName      types.String `tfsdk:"full_name"`
	Description   types.String `tfsdk:"description"`
	Visibility    types.String `tfsdk:"visibility"`
	ImmutableTags types.Bool   `tfsdk:"immutable_tags"`
	Retention     types.Object `tfsdk:"retention"`
	TagCount      types.Int64  `tfsdk:"tag_count"`
	StorageBytes  types.Int64  `tfsdk:"storage_bytes"`
	LastPushedAt  types.String `tfsdk:"last_pushed_at"`
	Status        types.String `tfsdk:"status"`
	CreatedAt     types.String `tfsdk:"created_at"`
}

var retentionTypes = map[string]attr.Type{
	"keep_last":           types.Int64Type,
	"untagged_after_days": types.Int64Type,
	"keep_pattern":        types.StringType,
}

// detailAttributes are the computed attributes of a repository, shared by
// both data sources so their descriptions stay the same.
func detailAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"namespace":      schema.StringAttribute{Computed: true, Description: "The namespace the repository is in."},
		"full_name":      schema.StringAttribute{Computed: true, Description: "The image name to tag and pull, such as \"registry.pantechdynamics.com/acme/api/server\"."},
		"description":    schema.StringAttribute{Computed: true, Description: "The repository's description."},
		"visibility":     schema.StringAttribute{Computed: true, Description: "\"private\" or \"public\" (anyone can pull)."},
		"immutable_tags": schema.BoolAttribute{Computed: true, Description: "Whether tags, once pushed, cannot move or be deleted."},
		"retention": schema.SingleNestedAttribute{Computed: true, Description: "The cleanup policy, or null for none.", Attributes: map[string]schema.Attribute{
			"keep_last":           schema.Int64Attribute{Computed: true, Description: "Only the newest this many tags are kept, or null."},
			"untagged_after_days": schema.Int64Attribute{Computed: true, Description: "Untagged images older than this many days are deleted, or null."},
			"keep_pattern":        schema.StringAttribute{Computed: true, Description: "Tags matching this glob are always kept, or null."},
		}},
		"tag_count":      schema.Int64Attribute{Computed: true, Description: "How many tags the repository holds."},
		"storage_bytes":  schema.Int64Attribute{Computed: true, Description: "Storage the repository's images use, in bytes."},
		"last_pushed_at": schema.StringAttribute{Computed: true, Description: "When an image was last pushed, in RFC 3339 UTC, to the second, or null."},
		"status":         schema.StringAttribute{Computed: true, Description: "\"active\", \"deleting\" or \"suspended\"."},
		"created_at":     schema.StringAttribute{Computed: true, Description: "When the repository was created, in RFC 3339 UTC, to the second."},
	}
}

// Metadata sets the type name: pantechdynamics_container_repositories.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_repositories"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	item := detailAttributes()
	item["id"] = schema.StringAttribute{Computed: true, Description: "Identifier of the repository (crr_...)."}
	item["name"] = schema.StringAttribute{Computed: true, Description: "Name inside the namespace, such as \"api/server\"."}
	resp.Schema = schema.Schema{
		Description: "Lists every repository in your organization's container registry namespace, including those created by a first push.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Always \"container_repositories\"."},
			"repositories": schema.ListNestedAttribute{
				Computed:     true,
				Description:  "The repositories, in the order the API lists them.",
				NestedObject: schema.NestedAttributeObject{Attributes: item},
			},
		},
	}
}

// Configure receives the API client from the provider.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configure(req, resp); ok {
		d.api = api
	}
}

// Read lists the repositories.
func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	repos, err := d.api.ListRepositories(ctx, "")
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading container repositories", err, readHints)
		return
	}
	m := listModel{ID: types.StringValue("container_repositories"), Repositories: make([]repositoryModel, 0, len(repos))}
	for i := range repos {
		item, diags := fromAPIResponse(&repos[i])
		resp.Diagnostics.Append(diags...)
		m.Repositories = append(m.Repositories, item)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// readHints explain the refusals a read can meet.
var readHints = map[string]string{
	client.CodeRegistryNamespaceNotSet: "Your organization has not claimed a registry namespace yet (pantechdynamics_container_registry_namespace).",
	client.CodeRegistryUnavailable:     "The container registry is not available on this platform yet.",
}

func configure(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) (repositoryAPI, bool) {
	if req.ProviderData == nil {
		return nil, false
	}
	api, ok := req.ProviderData.(repositoryAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return nil, false
	}
	return api, true
}

// fromAPIResponse maps one repository.
func fromAPIResponse(r *client.Repository) (repositoryModel, diag.Diagnostics) {
	retention := types.ObjectNull(retentionTypes)
	var diags diag.Diagnostics
	if !r.Retention.IsEmpty() {
		retention, diags = types.ObjectValue(retentionTypes, map[string]attr.Value{
			"keep_last":           resourcekit.OptionalInt64(r.Retention.KeepLast),
			"untagged_after_days": resourcekit.OptionalInt64(r.Retention.UntaggedAfterDays),
			"keep_pattern":        types.StringPointerValue(r.Retention.KeepPattern),
		})
	}
	return repositoryModel{
		ID: types.StringValue(r.ID), Name: types.StringValue(r.Name), Namespace: types.StringValue(r.Namespace), FullName: types.StringValue(r.FullName),
		Description: types.StringValue(r.Description), Visibility: types.StringValue(r.Visibility), ImmutableTags: types.BoolValue(r.ImmutableTags),
		Retention: retention, TagCount: types.Int64Value(r.TagCount), StorageBytes: types.Int64Value(r.StorageBytes),
		LastPushedAt: resourcekit.Timestamp(r.LastPushedAt), Status: types.StringValue(r.Status), CreatedAt: resourcekit.Timestamp(r.CreatedAt),
	}, diags
}
