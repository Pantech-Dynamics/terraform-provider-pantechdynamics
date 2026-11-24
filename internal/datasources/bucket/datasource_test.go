package bucket

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	buckets []client.Bucket
	stats   map[string]client.BucketStats
	tags    map[string][]client.BucketTag
}

func (f *fakeAPI) GetBucket(_ context.Context, id string) (*client.Bucket, error) {
	for i := range f.buckets {
		if f.buckets[i].ID == id {
			cp := f.buckets[i]
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("getting bucket: %w", &client.APIError{Status: 404, Code: client.CodeBucketNotFound, RequestID: "req_1"})
}

func (f *fakeAPI) ListBuckets(context.Context) ([]client.Bucket, error) {
	return append([]client.Bucket{}, f.buckets...), nil
}

func (f *fakeAPI) GetBucketStats(_ context.Context, id string) (*client.BucketStats, error) {
	st := f.stats[id]
	return &st, nil
}

func (f *fakeAPI) GetBucketTags(_ context.Context, id string) ([]client.BucketTag, error) {
	return append([]client.BucketTag{}, f.tags[id]...), nil
}

func fixture() *fakeAPI {
	created := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	return &fakeAPI{
		buckets: []client.Bucket{
			{
				ID: "bkt_1", Name: "uploads", Region: "af-abj", Endpoint: "https://s3.af-abj.pantechdynamics.com",
				QuotaBytes: 1 << 30, SizeBytes: 100, ObjectCount: 1, Versioning: true, ObjectLock: false,
				PublicPrefixes: []string{"public/"}, State: client.BucketActive, CreatedAt: &created,
			},
			{
				ID: "bkt_2", Name: "deleted-one", Region: "af-abj", State: client.BucketDeleting, CreatedAt: &created,
			},
		},
		stats: map[string]client.BucketStats{
			"bkt_1": {SizeBytes: 8192, ObjectCount: 12},
		},
		tags: map[string][]client.BucketTag{
			"bkt_1": {{Key: "env", Value: "prod"}},
		},
	}
}

func read(t *testing.T, ds datasource.DataSource, set map[string]string) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &sresp)
	if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	typ, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("schema is not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		vals[k] = tftypes.NewValue(tftypes.String, v)
	}
	cfg := tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	if v, ok := ds.(datasource.DataSourceWithValidateConfig); ok {
		var vresp datasource.ValidateConfigResponse
		v.ValidateConfig(ctx, datasource.ValidateConfigRequest{Config: cfg}, &vresp)
		resp.Diagnostics.Append(vresp.Diagnostics...)
		if resp.Diagnostics.HasError() {
			return resp
		}
	}
	ds.Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	return resp
}

func errText(resp datasource.ReadResponse) string {
	var b strings.Builder
	for _, e := range resp.Diagnostics.Errors() {
		b.WriteString(e.Summary() + " " + e.Detail() + "\n")
	}
	return b.String()
}

func TestMetadataAndConfigure(t *testing.T) {
	var meta datasource.MetadataResponse
	New().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &meta)
	if meta.TypeName != "pantechdynamics_bucket" {
		t.Fatalf("TypeName = %q", meta.TypeName)
	}
	var bad datasource.ConfigureResponse
	(&DataSource{}).Configure(ctx, datasource.ConfigureRequest{ProviderData: 42}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Fatal("wrong provider data must error")
	}
	c, err := client.New("https://api.example.com/public/v1", "PAN_x", "test")
	if err != nil {
		t.Fatal(err)
	}
	ds := &DataSource{}
	var ok datasource.ConfigureResponse
	ds.Configure(ctx, datasource.ConfigureRequest{ProviderData: c}, &ok)
	if ok.Diagnostics.HasError() || ds.api == nil {
		t.Fatalf("real client must satisfy bucketAPI: %v", ok.Diagnostics)
	}
}

func TestLookup(t *testing.T) {
	tests := []struct {
		name    string
		set     map[string]string
		wantID  string
		wantErr string
	}{
		{"by id", map[string]string{"id": "bkt_1"}, "bkt_1", ""},
		{"by name", map[string]string{"name": "uploads"}, "bkt_1", ""},
		{"deleting bucket ignored by name", map[string]string{"name": "deleted-one"}, "", "No bucket"},
		{"deleting bucket rejected by id", map[string]string{"id": "bkt_2"}, "", "being deleted"},
		{"missing id", map[string]string{"id": "bkt_9"}, "", "No bucket with this id"},
		{"bad id prefix", map[string]string{"id": "vm_1"}, "", "bkt_"},
		{"neither set", map[string]string{}, "", "exactly one"},
		{"both set", map[string]string{"id": "bkt_1", "name": "uploads"}, "", "exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := read(t, &DataSource{api: fixture()}, tt.set)
			if tt.wantErr != "" {
				if !strings.Contains(errText(resp), tt.wantErr) {
					t.Fatalf("errors = %s, want %q", errText(resp), tt.wantErr)
				}
				return
			}
			var m model
			resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
			if resp.Diagnostics.HasError() {
				t.Fatalf("diagnostics = %v", resp.Diagnostics)
			}
			if m.ID.ValueString() != tt.wantID || m.Name.ValueString() != "uploads" ||
				m.SizeBytes.ValueInt64() != 8192 || m.ObjectCount.ValueInt64() != 12 ||
				!m.Versioning.ValueBool() || len(m.Tags.Elements()) != 1 || len(m.PublicPrefixes.Elements()) != 1 {
				t.Fatalf("model = %+v", m)
			}
		})
	}
}
