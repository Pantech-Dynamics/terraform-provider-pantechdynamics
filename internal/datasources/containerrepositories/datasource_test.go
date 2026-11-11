package containerrepositories

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	repos []client.Repository
	err   error
	lastQ string
}

func (f *fakeAPI) ListRepositories(_ context.Context, q string) ([]client.Repository, error) {
	f.lastQ = q
	return f.repos, f.err
}

func i64(n int64) *int64 { return &n }

func fixture() *fakeAPI {
	return &fakeAPI{repos: []client.Repository{
		{ID: "crr_1", Namespace: "acme", Name: "api/server", FullName: "registry.pantechdynamics.com/acme/api/server", Visibility: "private",
			Retention: &client.RepositoryRetention{KeepLast: i64(50)}, TagCount: 3, Status: "active"},
		{ID: "crr_2", Namespace: "acme", Name: "web", Visibility: "public", Retention: &client.RepositoryRetention{}, Status: "active"},
	}}
}

// read drives a data source's ValidateConfig (when it has one) and Read.
func read(t *testing.T, ds datasource.DataSource, sel map[string]string) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &sresp)
	if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	typ, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	for k, v := range sel {
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

func TestListMapsEveryRepository(t *testing.T) {
	resp := read(t, &DataSource{api: fixture()}, nil)
	var m listModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(m.Repositories) != 2 || m.Repositories[0].Retention.IsNull() || !m.Repositories[1].Retention.IsNull() || m.Repositories[0].TagCount.ValueInt64() != 3 {
		t.Fatalf("model = %+v", m)
	}
}

func TestListEmptyIsAnEmptyList(t *testing.T) {
	resp := read(t, &DataSource{api: &fakeAPI{}}, nil)
	var m listModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Repositories == nil || len(m.Repositories) != 0 {
		t.Fatalf("repositories = %v", m.Repositories)
	}
}

func TestListExplainsAnUnclaimedNamespace(t *testing.T) {
	resp := read(t, &DataSource{api: &fakeAPI{err: &client.APIError{Status: 404, Code: client.CodeRegistryNamespaceNotSet}}}, nil)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not claimed") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestSingleLookup(t *testing.T) {
	tests := []struct {
		name    string
		sel     map[string]string
		wantID  string
		wantErr string
	}{
		{"by name", map[string]string{"name": "api/server"}, "crr_1", ""},
		{"by id", map[string]string{"id": "crr_2"}, "crr_2", ""},
		{"unknown name", map[string]string{"name": "db"}, "", "Available names: api/server, web."},
		{"both", map[string]string{"id": "crr_1", "name": "web"}, "", "exactly one"},
		{"neither", map[string]string{}, "", "exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := fixture()
			resp := read(t, &SingleDataSource{api: api}, tt.sel)
			var b strings.Builder
			for _, e := range resp.Diagnostics.Errors() {
				b.WriteString(e.Summary() + " " + e.Detail())
			}
			if tt.wantErr != "" {
				if !strings.Contains(b.String(), tt.wantErr) {
					t.Fatalf("errors = %s", b.String())
				}
				return
			}
			var m repositoryModel
			resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
			if resp.Diagnostics.HasError() || m.ID.ValueString() != tt.wantID || api.lastQ != tt.sel["name"] {
				t.Fatalf("diagnostics = %v, model = %+v, q = %q", resp.Diagnostics, m, api.lastQ)
			}
		})
	}
}
