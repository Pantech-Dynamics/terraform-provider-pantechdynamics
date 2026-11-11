package dnsrecord

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory recordAPI for zone example.com. Writes answer
// pending with the values in canonical form, as the backend does, and the
// record becomes settleTo on the next read.
type fakeAPI struct {
	records map[string]client.DNSRecord

	settleTo                                string
	zoneStatus                              string // "" is active
	createErr, getErr, updateErr, deleteErr error

	creates, updates int
	lastCreate       client.CreateDNSRecordRequest
	lastUpdate       client.UpdateDNSRecordRequest
	lastIfMatch      int64
}

func newFake() *fakeAPI {
	return &fakeAPI{records: map[string]client.DNSRecord{}, settleTo: client.DNSRecordActive}
}

func canonical(recordType string, values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		c, _ := dnsname.CanonicalValue(recordType, v)
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

func notFound() error {
	return fmt.Errorf("getting record: %w", &client.APIError{Status: 404, Code: client.CodeDNSRecordNotFound})
}

func (f *fakeAPI) CreateDNSRecord(_ context.Context, zoneID string, req client.CreateDNSRecordRequest) (*client.DNSRecord, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	rel, _ := dnsname.RelativeName("example.com", req.Name)
	fqdn := "example.com"
	if rel != "@" {
		fqdn = rel + ".example.com"
	}
	rec := client.DNSRecord{ID: "dnsr_1", ZoneID: zoneID, Name: rel, FQDN: fqdn, Type: req.Type, TTL: req.TTL, Values: canonical(req.Type, req.Values),
		AllowPrivate: req.AllowPrivate, Comment: strings.TrimSpace(req.Comment), Status: client.DNSRecordPending, Version: 1}
	f.records[rec.ID] = rec
	return &rec, nil
}

func (f *fakeAPI) GetDNSRecord(_ context.Context, _, id string) (*client.DNSRecord, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	rec, ok := f.records[id]
	if !ok {
		return nil, notFound()
	}
	if rec.Status == client.DNSRecordPending {
		rec.Status = f.settleTo
		if f.settleTo == client.DNSRecordFailed {
			code := "invalid_change"
			rec.FailureCode = &code
		}
		f.records[id] = rec
	}
	if rec.Status == client.DNSRecordDeleting {
		delete(f.records, id)
		return nil, notFound()
	}
	return &rec, nil
}

func (f *fakeAPI) ListDNSRecords(_ context.Context, _ string, filter client.DNSRecordFilter) ([]client.DNSRecord, error) {
	var out []client.DNSRecord
	for _, r := range f.records {
		if filter.Type == "" || r.Type == filter.Type {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeAPI) UpdateDNSRecord(_ context.Context, _, id string, version int64, req client.UpdateDNSRecordRequest) (*client.DNSRecord, error) {
	f.updates++
	f.lastUpdate, f.lastIfMatch = req, version
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	rec, ok := f.records[id]
	if !ok {
		return nil, notFound()
	}
	if rec.Version != version {
		return nil, &client.APIError{Status: 412, Code: client.CodeVersionConflict}
	}
	if req.TTL != nil {
		rec.TTL = *req.TTL
	}
	if req.Values != nil {
		rec.Values = canonical(rec.Type, *req.Values)
	}
	if req.Comment != nil {
		rec.Comment = *req.Comment
	}
	rec.Version++
	rec.Status = client.DNSRecordPending
	f.records[id] = rec
	return &rec, nil
}

func (f *fakeAPI) DeleteDNSRecord(_ context.Context, _, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	rec, ok := f.records[id]
	if !ok {
		return notFound()
	}
	rec.Status = client.DNSRecordDeleting
	f.records[id] = rec
	return nil
}

func (f *fakeAPI) GetDNSZone(_ context.Context, id string) (*client.DNSZone, error) {
	status := f.zoneStatus
	if status == "" {
		status = client.DNSZoneActive
	}
	return &client.DNSZone{ID: id, Name: "example.com", Status: status}, nil
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	for range 3 {
		ok, err := done(ctx)
		if err != nil || ok {
			return err
		}
	}
	return context.DeadlineExceeded
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func strSet(vals ...string) tftypes.Value {
	elems := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, Str(v))
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
}

func boolVal(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }

// planFor is the plan of a record set, the computed attributes unknown.
func planFor(s schema.Schema, id tftypes.Value, name, typ string, ttl int64, values ...string) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": id, "zone_id": Str("dnsz_1"), "name": Str(name), "type": Str(typ), "ttl": Num(ttl), "values": strSet(values...),
		"allow_private": boolVal(false), "comment": Str(""), "fqdn": UnknownStr(), "status": UnknownStr(), "version": Unknown(tftypes.Number),
	})
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func values(t *testing.T, m model) []string {
	t.Helper()
	var out []string
	if d := m.Values.ElementsAs(Ctx, &out, false); d.HasError() {
		t.Fatal(d)
	}
	slices.Sort(out)
	return out
}

func create(t *testing.T, api *fakeAPI, name, typ string, vals ...string) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, UnknownStr(), name, typ, 300, vals...)}, resp)
	return resp
}

// The backend stores "Mail.Example.com" as "mail.example.com."; state keeps the
// configured spelling so the next plan shows no change.
func TestCreateKeepsTheConfiguredSpellingOfEquivalentValues(t *testing.T) {
	api := newFake()
	resp := create(t, api, "WWW", "CNAME", "Target.Example.com")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if got := values(t, m); len(got) != 1 || got[0] != "Target.Example.com" {
		t.Fatalf("values = %v, want the configured spelling", got)
	}
	if m.Name.ValueString() != "WWW" || m.FQDN.ValueString() != "www.example.com" || m.Status.ValueString() != "active" || m.Version.ValueInt64() != 1 {
		t.Fatalf("model = %+v", m)
	}
	if api.records["dnsr_1"].Values[0] != "target.example.com." {
		t.Fatal("the fake must store the canonical form")
	}
}

func TestCreateOfARefusedChangeKeepsTheIDAndNamesTheCode(t *testing.T) {
	api := newFake()
	api.settleTo = client.DNSRecordFailed
	resp := create(t, api, "www", "A", "203.0.113.10")
	if !strings.Contains(ErrorText(resp.Diagnostics), "invalid_change") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).Status.ValueString() != "failed" {
		t.Fatal("the created record must stay in state, as failed")
	}
}

func TestCreateRefusalsAreExplained(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"exists", &client.APIError{Status: 409, Code: client.CodeDNSRecordExists}, "<zone_id>/<name>/<type>"},
		{"cname conflict", &client.APIError{Status: 409, Code: "DNS_CNAME_CONFLICT"}, "stand alone"},
		{"value", &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "values[0]", Code: "INVALID", Message: "must be an IPv4 address"}}}, "must be an IPv4 address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFake()
			api.createErr = tt.err
			resp := create(t, api, "www", "A", "203.0.113.10")
			if !strings.Contains(ErrorText(resp.Diagnostics), tt.want) || !IsRemoved(resp.State) {
				t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
			}
		})
	}
}

// seeded returns a fake holding an active A record dnsr_1 at version v.
func seeded(v int64, vals ...string) *fakeAPI {
	api := newFake()
	api.records["dnsr_1"] = client.DNSRecord{ID: "dnsr_1", ZoneID: "dnsz_1", Name: "www", FQDN: "www.example.com", Type: "A", TTL: 300, Values: vals, Status: client.DNSRecordActive, Version: v}
	return api
}

func stateFor(s schema.Schema, version int64, vals ...string) tfsdk.State {
	return State(s, map[string]tftypes.Value{
		"id": Str("dnsr_1"), "zone_id": Str("dnsz_1"), "name": Str("www"), "type": Str("A"), "ttl": Num(300), "values": strSet(vals...),
		"allow_private": boolVal(false), "comment": Str(""), "fqdn": Str("www.example.com"), "status": Str("active"), "version": Num(version),
	})
}

func TestReadTakesAValueChangedOutsideTerraform(t *testing.T) {
	api := seeded(2, "203.0.113.99")
	s := testSchema(t)
	resp := &resource.ReadResponse{State: stateFor(s, 1, "203.0.113.10")}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s, 1, "203.0.113.10")}, resp)
	m := getModel(t, resp.State)
	if got := values(t, m); len(got) != 1 || got[0] != "203.0.113.99" || m.Version.ValueInt64() != 2 {
		t.Fatalf("model = %+v, values = %v", m, got)
	}
}

func TestReadRemovesARecordGoneOutsideTerraform(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	resp := &resource.ReadResponse{State: stateFor(s, 1, "203.0.113.10")}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s, 1, "203.0.113.10")}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestUpdateSendsIfMatchAndOnlyTheChange(t *testing.T) {
	api := seeded(4, "203.0.113.10")
	s := testSchema(t)
	resp := &resource.UpdateResponse{State: stateFor(s, 4, "203.0.113.10")}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: planFor(s, Str("dnsr_1"), "www", "A", 300, "203.0.113.10", "203.0.113.11"), State: stateFor(s, 4, "203.0.113.10")}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.lastIfMatch != 4 || api.lastUpdate.TTL != nil || api.lastUpdate.Values == nil || len(*api.lastUpdate.Values) != 2 {
		t.Fatalf("sent %+v with If-Match %d", api.lastUpdate, api.lastIfMatch)
	}
	m := getModel(t, resp.State)
	if m.Version.ValueInt64() != 5 || m.Status.ValueString() != "active" || len(values(t, m)) != 2 {
		t.Fatalf("model = %+v", m)
	}
}

func TestUpdateOnAStaleVersionIsExplained(t *testing.T) {
	api := seeded(9, "203.0.113.10")
	s := testSchema(t)
	resp := &resource.UpdateResponse{State: stateFor(s, 4, "203.0.113.10")}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: planFor(s, Str("dnsr_1"), "www", "A", 600, "203.0.113.10"), State: stateFor(s, 4, "203.0.113.10")}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "changed outside Terraform") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestDeleteWaitsUntilGone(t *testing.T) {
	for _, seed := range []bool{true, false} {
		api := newFake()
		if seed {
			api = seeded(1, "203.0.113.10")
		}
		s := testSchema(t)
		resp := &resource.DeleteResponse{State: stateFor(s, 1, "203.0.113.10")}
		(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s, 1, "203.0.113.10")}, resp)
		if resp.Diagnostics.HasError() || len(api.records) != 0 {
			t.Fatalf("seed=%v: diagnostics = %v, records = %v", seed, resp.Diagnostics, api.records)
		}
	}
}

func TestImport(t *testing.T) {
	api := seeded(1, "203.0.113.10")
	api.records["dnsr_soa"] = client.DNSRecord{ID: "dnsr_soa", ZoneID: "dnsz_1", Name: "@", FQDN: "example.com", Type: "NS", Managed: true}
	tests := []struct {
		id, wantID, wantErr string
	}{
		{"dnsz_1/dnsr_1", "dnsr_1", ""},
		{"dnsz_1/www/A", "dnsr_1", ""},
		{"dnsz_1/WWW.example.com./a", "dnsr_1", ""},
		{"dnsz_1/mail/A", "", "no A record set"},
		{"dnsz_1/@/NS", "", "managed by Pantech DNS"},
		{"dnsr_1", "", "Invalid import id"},
		{"dnsz_1/vm_1", "", "Invalid import id"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			s := testSchema(t)
			resp := &resource.ImportStateResponse{State: EmptyState(s)}
			(&Resource{api: api}).ImportState(Ctx, resource.ImportStateRequest{ID: tt.id}, resp)
			if got := ErrorText(resp.Diagnostics); (tt.wantErr == "") != (got == "") || !strings.Contains(got, tt.wantErr) {
				t.Fatalf("diagnostics = %q", got)
			}
			if tt.wantErr == "" {
				m := getModel(t, resp.State)
				if m.ID.ValueString() != tt.wantID || m.ZoneID.ValueString() != "dnsz_1" {
					t.Fatalf("model = %+v", m)
				}
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name, recordName, typ string
		allowPrivate          bool
		values                []string
		wantErr               string
	}{
		{"valid", "www", "A", false, []string{"203.0.113.10"}, ""},
		{"bad address", "www", "A", false, []string{"not-an-ip"}, "IPv4"},
		{"private refused", "www", "A", false, []string{"10.0.0.5"}, "allow_private"},
		{"private allowed", "www", "A", true, []string{"10.0.0.5"}, ""},
		{"two cnames", "www", "CNAME", false, []string{"a.example.com", "b.example.com"}, "exactly one"},
		{"cname at apex", "@", "CNAME", false, []string{"a.example.com"}, "apex"},
		{"bad mx", "@", "MX", false, []string{"mail.example.com"}, "priority host"},
		{"txt", "_dmarc", "TXT", false, []string{"v=DMARC1; p=none"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSchema(t)
			cfg := tfsdk.Config{Schema: s, Raw: Object(s, map[string]tftypes.Value{
				"zone_id": Str("dnsz_1"), "name": Str(tt.recordName), "type": Str(tt.typ), "values": strSet(tt.values...), "allow_private": boolVal(tt.allowPrivate),
			})}
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
			if got := ErrorText(resp.Diagnostics); (tt.wantErr == "") != (got == "") || !strings.Contains(got, tt.wantErr) {
				t.Fatalf("diagnostics = %q", got)
			}
		})
	}
}

// In a zone awaiting delegation, the record stays pending by design: the wait
// ends with a warning, not an error, and the record is saved.
func TestCreateInAZoneAwaitingDelegationWarns(t *testing.T) {
	api := newFake()
	api.settleTo, api.zoneStatus = client.DNSRecordPending, client.DNSZoneAwaitingDelegation
	resp := create(t, api, "www", "A", "203.0.113.10")
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 || !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "awaiting_delegation") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if m := getModel(t, resp.State); m.Status.ValueString() != "pending" || m.ID.ValueString() != "dnsr_1" {
		t.Fatalf("model = %+v", m)
	}
}
