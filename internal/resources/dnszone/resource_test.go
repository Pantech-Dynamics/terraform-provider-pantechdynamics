package dnszone

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory zoneAPI. A new zone answers pending and becomes
// settleTo on the first read. A non-nil *Err is returned by the matching method.
type fakeAPI struct {
	zones map[string]client.DNSZone

	settleTo                                string
	failureCode                             string
	lingerAfterDelete                       bool
	createErr, getErr, updateErr, deleteErr error

	creates, deletes int
	lastCreate       client.CreateDNSZoneRequest
	lastUpdate       client.UpdateDNSZoneRequest
}

func newFake() *fakeAPI {
	return &fakeAPI{zones: map[string]client.DNSZone{}, settleTo: client.DNSZoneAwaitingDelegation}
}

func at(sec int) *time.Time {
	t := time.Date(2026, 11, 10, 10, 0, sec, 123456000, time.UTC)
	return &t
}

func (f *fakeAPI) CreateDNSZone(_ context.Context, req client.CreateDNSZoneRequest) (*client.DNSZone, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	z := client.DNSZone{ID: "dnsz_1", Name: req.Name, Description: req.Description, Status: client.DNSZonePending, NameServers: []string{}, RecordCount: 2, CreatedAt: at(0), Version: 1}
	f.zones[z.ID] = z
	return &z, nil
}

func (f *fakeAPI) GetDNSZone(_ context.Context, id string) (*client.DNSZone, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	z, ok := f.zones[id]
	if !ok {
		return nil, fmt.Errorf("getting zone: %w", &client.APIError{Status: 404, Code: client.CodeDNSZoneNotFound})
	}
	if z.Status == client.DNSZonePending {
		z.Status = f.settleTo
		z.NameServers = []string{"ns-1.example-dns.org", "ns-2.example-dns.com"}
		if f.failureCode != "" {
			code := f.failureCode
			z.FailureCode = &code
		}
		f.zones[id] = z
	}
	return &z, nil
}

func (f *fakeAPI) UpdateDNSZone(_ context.Context, id string, req client.UpdateDNSZoneRequest) (*client.DNSZone, error) {
	f.lastUpdate = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	z := f.zones[id]
	z.Description = req.Description
	f.zones[id] = z
	return &z, nil
}

func (f *fakeAPI) DeleteDNSZone(_ context.Context, id string) (*client.DNSZone, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	z, ok := f.zones[id]
	if !ok {
		return nil, fmt.Errorf("deleting zone: %w", &client.APIError{Status: 404, Code: client.CodeDNSZoneNotFound})
	}
	if f.lingerAfterDelete {
		z.Status = client.DNSZoneDeleted
		f.zones[id] = z
	} else {
		delete(f.zones, id)
	}
	return &z, nil
}

func (f *fakeAPI) QuoteDNSZones(context.Context) (*client.DNSZoneQuote, error) {
	return &client.DNSZoneQuote{FreeZones: 5, LiveZones: int64(len(f.zones)), Currency: "USD", MonthlyPerZoneMinor: 50}, nil
}

// WaitUntil polls the done check a few times, as the real loop would.
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

func planFor(s schema.Schema, name, description string) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "name": Str(name), "description": Str(description), "status": UnknownStr(), "failure_code": UnknownStr(),
		"name_servers": Unknown(tftypes.List{ElementType: tftypes.String}), "domain_id": UnknownStr(), "record_count": Unknown(tftypes.Number), "created_at": UnknownStr(),
	})
}

func stateFor(s schema.Schema) tfsdk.State {
	return State(s, map[string]tftypes.Value{"id": Str("dnsz_1"), "name": Str("example.com"), "description": Str("")})
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, "example.com", "Marketing")}, resp)
	return resp
}

func TestCreateWaitsForTheNameServers(t *testing.T) {
	api := newFake()
	resp := create(t, api)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	var servers []string
	m.NameServers.ElementsAs(Ctx, &servers, false)
	if m.ID.ValueString() != "dnsz_1" || m.Status.ValueString() != client.DNSZoneAwaitingDelegation || len(servers) != 2 {
		t.Fatalf("model = %+v", m)
	}
	if !m.DomainID.IsNull() || !m.FailureCode.IsNull() || m.RecordCount.ValueInt64() != 2 || m.CreatedAt.ValueString() != "2026-11-10T10:00:00Z" {
		t.Fatalf("computed attributes = %+v", m)
	}
	if api.lastCreate.Name != "example.com" || api.lastCreate.Description != "Marketing" {
		t.Fatalf("create sent %+v", api.lastCreate)
	}
}

// A zone that fails while being created keeps its id in state, so the next
// apply can replace it, and the error names the failure code.
func TestCreateOfAFailedZoneKeepsTheIDAndNamesTheCode(t *testing.T) {
	api := newFake()
	api.settleTo, api.failureCode = client.DNSZoneFailed, "provider_rejected"
	resp := create(t, api)
	if !strings.Contains(ErrorText(resp.Diagnostics), "provider_rejected") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "dnsz_1" {
		t.Fatal("the created zone must stay in state")
	}
}

func TestCreateRefusalsAreExplained(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"taken", &client.APIError{Status: 409, Code: client.CodeDNSZoneNameTaken}, "terraform import"},
		{"limit", &client.APIError{Status: 403, Code: "DNS_ZONE_LIMIT_REACHED"}, "(25)"},
		{"unavailable", &client.APIError{Status: 503, Code: client.CodeDNSUnavailable}, "not available"},
		{"field error", &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "name", Code: "PUBLIC_SUFFIX", Message: "is a public suffix"}}}, "PUBLIC_SUFFIX"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFake()
			api.createErr = tt.err
			resp := create(t, api)
			if !strings.Contains(ErrorText(resp.Diagnostics), tt.want) || !IsRemoved(resp.State) {
				t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
			}
		})
	}
}

func TestReadRemovesAZoneGoneOutsideTerraform(t *testing.T) {
	for _, lingers := range []bool{false, true} {
		api := newFake()
		api.lingerAfterDelete = lingers
		s := testSchema(t)
		api.zones["dnsz_1"] = client.DNSZone{ID: "dnsz_1", Name: "example.com", Status: client.DNSZoneActive}
		_, _ = api.DeleteDNSZone(Ctx, "dnsz_1")
		resp := &resource.ReadResponse{State: stateFor(s)}
		(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
		if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
			t.Fatalf("lingers=%v: diagnostics = %v, removed = %v", lingers, resp.Diagnostics, IsRemoved(resp.State))
		}
	}
}

func TestReadRefreshesState(t *testing.T) {
	api := newFake()
	dom := "dom_1"
	api.zones["dnsz_1"] = client.DNSZone{ID: "dnsz_1", Name: "example.com", Description: "changed", Status: client.DNSZoneActive, DomainID: &dom, NameServers: []string{"ns-1.example-dns.org"}}
	s := testSchema(t)
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	m := getModel(t, resp.State)
	if m.Description.ValueString() != "changed" || m.Status.ValueString() != "active" || m.DomainID.ValueString() != "dom_1" {
		t.Fatalf("model = %+v", m)
	}
}

func TestUpdateSendsTheDescription(t *testing.T) {
	api := newFake()
	api.zones["dnsz_1"] = client.DNSZone{ID: "dnsz_1", Name: "example.com", Status: client.DNSZoneActive}
	s := testSchema(t)
	plan := Plan(s, map[string]tftypes.Value{"id": Str("dnsz_1"), "name": Str("example.com"), "description": Str("new"),
		"status": UnknownStr(), "failure_code": UnknownStr(), "name_servers": Unknown(tftypes.List{ElementType: tftypes.String}), "domain_id": UnknownStr(), "record_count": Unknown(tftypes.Number), "created_at": UnknownStr()})
	resp := &resource.UpdateResponse{State: stateFor(s)}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || api.lastUpdate.Description != "new" || getModel(t, resp.State).Description.ValueString() != "new" {
		t.Fatalf("diagnostics = %v, sent %+v", resp.Diagnostics, api.lastUpdate)
	}
}

func TestDeleteWaitsUntilGone(t *testing.T) {
	tests := []struct {
		name    string
		seed    bool
		linger  bool
		err     error
		wantErr string
	}{
		{"removed", true, false, nil, ""},
		{"readable as deleted", true, true, nil, ""},
		{"already gone", false, false, nil, ""},
		{"in use by a domain", true, false, &client.APIError{Status: 409, Code: "DNS_ZONE_IN_USE_BY_DOMAIN"}, "Point the domain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFake()
			api.lingerAfterDelete, api.deleteErr = tt.linger, tt.err
			if tt.seed {
				api.zones["dnsz_1"] = client.DNSZone{ID: "dnsz_1", Name: "example.com", Status: client.DNSZoneActive}
			}
			s := testSchema(t)
			resp := &resource.DeleteResponse{State: stateFor(s)}
			(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
			if got := ErrorText(resp.Diagnostics); (tt.wantErr == "") != (got == "") || !strings.Contains(got, tt.wantErr) {
				t.Fatalf("diagnostics = %q", got)
			}
		})
	}
}

func TestImportChecksThePrefix(t *testing.T) {
	s := testSchema(t)
	for id, wantErr := range map[string]bool{"dnsz_1": false, "dom_1": true} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%s: diagnostics = %v", id, resp.Diagnostics)
		}
	}
}

// Past the free zones, a create warns with the price; before, it is silent.
func TestModifyPlanWarnsPastTheFreeZones(t *testing.T) {
	s := testSchema(t)
	for live, wantWarning := range map[int]bool{4: false, 5: true} {
		api := newFake()
		for i := range live {
			api.zones[fmt.Sprintf("dnsz_%d", i+10)] = client.DNSZone{}
		}
		plan := planFor(s, "example.com", "")
		resp := &resource.ModifyPlanResponse{Plan: plan}
		(&Resource{api: api}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: plan, State: EmptyState(s)}, resp)
		if got := resp.Diagnostics.WarningsCount() == 1; got != wantWarning {
			t.Errorf("%d live zones: warnings = %v", live, resp.Diagnostics)
		}
		if wantWarning && !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "USD 0.50 a month") {
			t.Errorf("warning = %s", resp.Diagnostics.Warnings()[0].Detail())
		}
	}
}

func TestProviderLimitIsExplained(t *testing.T) {
	code := "provider_limit"
	if err := failedError(&client.DNSZone{ID: "dnsz_1", FailureCode: &code}); !strings.Contains(err.Error(), "name-server pair") {
		t.Fatalf("err = %v", err)
	}
}
