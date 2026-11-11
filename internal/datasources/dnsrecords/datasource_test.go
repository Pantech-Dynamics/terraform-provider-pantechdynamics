package dnsrecords

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

type fakeAPI struct {
	records    []client.DNSRecord
	err        error
	lastFilter client.DNSRecordFilter
}

func (f *fakeAPI) ListDNSRecords(_ context.Context, _ string, filter client.DNSRecordFilter) ([]client.DNSRecord, error) {
	f.lastFilter = filter
	return f.records, f.err
}

var records = []client.DNSRecord{
	{ID: "dnsr_soa", Name: "@", FQDN: "example.com", Type: "SOA", TTL: 900, Values: []string{"ns-1.example-dns.org. hostmaster.example.com. 1 7200 900 1209600 86400"}, Managed: true, Status: "active"},
	{ID: "dnsr_1", Name: "www", FQDN: "www.example.com", Type: "A", TTL: 300, Values: []string{"203.0.113.10", "203.0.113.11"}, Status: "active", Version: 2},
	{ID: "dnsr_2", Name: "mail", FQDN: "mail.example.com", Type: "A", TTL: 300, Status: "pending"},
}

func cfg(typ, name string) model {
	m := model{ZoneID: types.StringValue("dnsz_1"), Type: types.StringNull(), Name: types.StringNull()}
	if typ != "" {
		m.Type = types.StringValue(typ)
	}
	if name != "" {
		m.Name = types.StringValue(name)
	}
	return m
}

func TestFromAPIResponse(t *testing.T) {
	tests := []struct {
		name, typ, filterName string
		wantIDs               string
	}{
		{"everything", "", "", "dnsr_soa,dnsr_1,dnsr_2"},
		{"by name", "", "WWW", "dnsr_1"},
		{"by fqdn", "", "www.example.com.", "dnsr_1"},
		{"apex", "", "@", "dnsr_soa"},
		{"no match", "", "ftp", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, diags := fromAPIResponse(context.Background(), cfg(tt.typ, tt.filterName), records)
			if diags.HasError() {
				t.Fatal(diags)
			}
			ids := make([]string, 0, len(m.Records))
			for _, r := range m.Records {
				ids = append(ids, r.ID.ValueString())
			}
			if strings.Join(ids, ",") != tt.wantIDs || m.Records == nil {
				t.Fatalf("ids = %v, want %s", ids, tt.wantIDs)
			}
		})
	}
	m, _ := fromAPIResponse(context.Background(), cfg("", "www"), records)
	if r := m.Records[0]; len(r.Values.Elements()) != 2 || r.Version.ValueInt64() != 2 || r.Managed.ValueBool() {
		t.Fatalf("record = %+v", r)
	}
	if m, _ := fromAPIResponse(context.Background(), cfg("", "mail"), records); len(m.Records[0].Values.Elements()) != 0 || m.Records[0].Values.IsNull() {
		t.Fatal("a record with no values must show an empty list")
	}
}

// read drives Read with the given configuration.
func read(t *testing.T, api recordsAPI, set map[string]string) datasource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	var sresp datasource.SchemaResponse
	New().Schema(ctx, datasource.SchemaRequest{}, &sresp)
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
	for k, v := range set {
		vals[k] = tftypes.NewValue(tftypes.String, v)
	}
	cfg := tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&DataSource{api: api}).Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	return resp
}

func TestReadSendsTheTypeFilter(t *testing.T) {
	api := &fakeAPI{records: records[1:2]}
	resp := read(t, api, map[string]string{"zone_id": "dnsz_1", "type": "A"})
	var m model
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &m)...)
	if resp.Diagnostics.HasError() || api.lastFilter.Type != "A" || len(m.Records) != 1 || m.ID.ValueString() != "dnsz_1/A/" {
		t.Fatalf("diagnostics = %v, filter = %+v, model = %+v", resp.Diagnostics, api.lastFilter, m)
	}
}

func TestReadExplainsAMissingZone(t *testing.T) {
	resp := read(t, &fakeAPI{err: &client.APIError{Status: 404, Code: client.CodeDNSZoneNotFound}}, map[string]string{"zone_id": "dnsz_9"})
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "Check zone_id") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	resp = read(t, &fakeAPI{err: errors.New("boom")}, map[string]string{"zone_id": "dnsz_1"})
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
}
