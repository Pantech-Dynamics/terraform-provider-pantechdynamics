// Package bucket implements the pantechdynamics_bucket data source.
package bucket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const idPrefix = "bkt_"

// bucketAPI is the part of the API client this data source needs.
type bucketAPI interface {
	GetBucket(ctx context.Context, id string) (*client.Bucket, error)
	ListBuckets(ctx context.Context) ([]client.Bucket, error)
	GetBucketStats(ctx context.Context, id string) (*client.BucketStats, error)
	GetBucketTags(ctx context.Context, id string) ([]client.BucketTag, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &DataSource{}
	_ datasource.DataSourceWithValidateConfig = &DataSource{}
)

// DataSource looks up one object storage bucket by id or name.
type DataSource struct {
	api bucketAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Region         types.String `tfsdk:"region"`
	Endpoint       types.String `tfsdk:"endpoint"`
	State          types.String `tfsdk:"state"`
	QuotaBytes     types.Int64  `tfsdk:"quota_bytes"`
	SizeBytes      types.Int64  `tfsdk:"size_bytes"`
	ObjectCount    types.Int64  `tfsdk:"object_count"`
	Versioning     types.Bool   `tfsdk:"versioning"`
	ObjectLock     types.Bool   `tfsdk:"object_lock"`
	PublicPrefixes types.List   `tfsdk:"public_prefixes"`
	Tags           types.Map    `tfsdk:"tags"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_bucket.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one S3-compatible object storage bucket by id or name, along with its live storage usage and tags. Deleted buckets are not found.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Identifier of the bucket, starting with bkt_. Set exactly one of id and name.",
				Optional:    true,
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "Name of the bucket. Set exactly one of id and name.",
				Optional:    true,
				Computed:    true,
			},
			"region": schema.StringAttribute{
				Description: "Region code where the bucket resides, such as \"af-abj\".",
				Computed:    true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The S3 endpoint URL for this bucket's region.",
				Computed:    true,
			},
			"state": schema.StringAttribute{
				Description: "Current bucket state: \"provisioning\", \"active\", \"updating\", \"deleting\", \"failed\", or \"unknown\".",
				Computed:    true,
			},
			"quota_bytes": schema.Int64Attribute{
				Description: "Size limit in bytes; 0 means no limit.",
				Computed:    true,
			},
			"size_bytes": schema.Int64Attribute{
				Description: "Live stored bytes in the bucket.",
				Computed:    true,
			},
			"object_count": schema.Int64Attribute{
				Description: "Live number of objects stored in the bucket.",
				Computed:    true,
			},
			"versioning": schema.BoolAttribute{
				Description: "Whether object versioning is enabled.",
				Computed:    true,
			},
			"object_lock": schema.BoolAttribute{
				Description: "Whether object lock is enabled on the bucket.",
				Computed:    true,
			},
			"public_prefixes": schema.ListAttribute{
				Description: "Key prefixes anyone may read without a key. Empty when wholly private.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags set on the bucket.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "When the bucket was created, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
		},
	}
}

// Configure receives the API client from the provider.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(bucketAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// ValidateConfig requires exactly one of id and name, and checks the bkt_
// prefix when id is known.
func (d *DataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	lookup.ValidateIDOrName(ctx, req.Config, "bucket", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	if !id.IsNull() && !id.IsUnknown() && !strings.HasPrefix(id.ValueString(), idPrefix) {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid bucket id",
			fmt.Sprintf("Expected an id starting with %q, got %q.", idPrefix, id.ValueString()))
	}
}

// Read looks up the bucket by id or name, then loads live storage stats and tags.
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	b, err := d.find(ctx, cfg)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Bucket not found", err, readHints)
		return
	}

	sizeBytes, objectCount := b.SizeBytes, b.ObjectCount
	stats, err := d.api.GetBucketStats(ctx, b.ID)
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading bucket stats", err, readHints)
		return
	}
	if stats != nil {
		sizeBytes, objectCount = stats.SizeBytes, stats.ObjectCount
	}

	rawTags, err := d.api.GetBucketTags(ctx, b.ID)
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading bucket tags", err, readHints)
		return
	}

	prefixes := b.PublicPrefixes
	if prefixes == nil {
		prefixes = []string{}
	}
	prefixList, diags := types.ListValueFrom(ctx, types.StringType, prefixes)
	resp.Diagnostics.Append(diags...)

	tagMap := make(map[string]string, len(rawTags))
	for _, t := range rawTags {
		tagMap[t.Key] = t.Value
	}
	tagsVal, diags := types.MapValueFrom(ctx, types.StringType, tagMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, model{
		ID:             types.StringValue(b.ID),
		Name:           types.StringValue(b.Name),
		Region:         types.StringValue(b.Region),
		Endpoint:       types.StringValue(b.Endpoint),
		State:          types.StringValue(b.State),
		QuotaBytes:     types.Int64Value(b.QuotaBytes),
		SizeBytes:      types.Int64Value(sizeBytes),
		ObjectCount:    types.Int64Value(objectCount),
		Versioning:     types.BoolValue(b.Versioning),
		ObjectLock:     types.BoolValue(b.ObjectLock),
		PublicPrefixes: prefixList,
		Tags:           tagsVal,
		CreatedAt:      resourcekit.Timestamp(b.CreatedAt),
	})...)
}

func (d *DataSource) find(ctx context.Context, cfg model) (*client.Bucket, error) {
	if id := cfg.ID.ValueString(); id != "" {
		b, err := d.api.GetBucket(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.State == client.BucketDeleting {
			return nil, fmt.Errorf("bucket %q is being deleted", id)
		}
		return b, nil
	}

	buckets, err := d.api.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	live := make([]client.Bucket, 0, len(buckets))
	for _, b := range buckets {
		if b.State != client.BucketDeleting {
			live = append(live, b)
		}
	}
	picked, err := lookup.Pick(live, "bucket", "", cfg.Name.ValueString(),
		func(b client.Bucket) string { return b.ID },
		func(b client.Bucket) string { return b.Name },
	)
	if err != nil {
		return nil, err
	}
	return &picked, nil
}

var readHints = map[string]string{
	client.CodeBucketNotFound:          "No bucket with this id exists in your organization.",
	client.CodeObjectStorageNotEnabled: "Object storage is in closed beta and is not switched on for your organization.",
}
