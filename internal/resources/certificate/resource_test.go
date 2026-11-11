package certificate

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

func ptr(s string) *string { return &s }

// fakeAPI is an in-memory certificateAPI. A request answers pending; each
// read moves it one status along steps.
type fakeAPI struct {
	certs     map[string]client.Certificate
	steps     []string
	failure   string
	reads     int
	deleteErr error
	lastReq   client.RequestCertificateRequest
}

func newFake(steps ...string) *fakeAPI {
	return &fakeAPI{certs: map[string]client.Certificate{}, steps: steps}
}

func (f *fakeAPI) RequestCertificate(_ context.Context, req client.RequestCertificateRequest) (*client.Certificate, error) {
	f.lastReq = req
	c := client.Certificate{ID: "cert_1", Kind: "public", Names: req.Names, CommonName: ptr(req.Names[0]), KeyType: "ecdsa-p256", Exportable: req.Exportable,
		Status: client.CertificatePendingValidation, PendingReason: ptr("awaiting_cname"), Version: 1,
		Validations: []client.CertificateValidation{
			{Name: "example.com", Method: "pantech_dns", ZoneID: ptr("dnsz_1"), Status: "ready"},
			{Name: "api.example.org", Method: "cname", CNAMEName: ptr("_acme-challenge.api.example.org"), CNAMETarget: ptr("k3x.acme.example.net"), Status: "pending"},
			{Name: "*.api.example.org", Method: "cname", CNAMEName: ptr("_acme-challenge.api.example.org"), CNAMETarget: ptr("k3x.acme.example.net"), Status: "pending"},
		}}
	if req.KeyType != "" {
		c.KeyType = req.KeyType
	}
	f.certs[c.ID] = c
	return &c, nil
}

func (f *fakeAPI) GetCertificate(_ context.Context, id string) (*client.Certificate, error) {
	c, ok := f.certs[id]
	if !ok {
		return nil, fmt.Errorf("get: %w", &client.APIError{Status: 404, Code: client.CodeCertificateNotFound})
	}
	if f.reads < len(f.steps) {
		c.Status = f.steps[f.reads]
		if c.Status == client.CertificateIssued {
			c.PendingReason, c.SerialNumber, c.Issuer = nil, ptr("0a1b"), ptr("Example CA")
		}
		if c.Status == client.CertificateFailed {
			c.PendingReason, c.FailureCode = nil, ptr(f.failure)
		}
		f.certs[id] = c
	}
	f.reads++
	return &c, nil
}

func (f *fakeAPI) UpdateCertificateDescription(_ context.Context, id, d string) (*client.Certificate, error) {
	c := f.certs[id]
	c.Description = d
	f.certs[id] = c
	return &c, nil
}

func (f *fakeAPI) DeleteCertificate(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.certs[id]; !ok {
		return &client.APIError{Status: 404, Code: client.CodeCertificateNotFound}
	}
	delete(f.certs, id)
	return nil
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	for range 4 {
		ok, err := done(ctx)
		if err != nil || ok {
			return err
		}
	}
	return context.DeadlineExceeded
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func strList(vals ...string) tftypes.Value {
	elems := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, Str(v))
	}
	return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, elems)
}

func planFor(t *testing.T, wait bool) tfsdk.Plan {
	s := testSchema(t)
	recordType, ok := s.Attributes["validation_records"].GetType().TerraformType(Ctx).(tftypes.List)
	if !ok {
		t.Fatal("validation_records is not a list")
	}
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "names": strList("example.com", "api.example.org", "*.api.example.org"), "key_type": Str("ecdsa-p256"),
		"exportable": tftypes.NewValue(tftypes.Bool, true), "description": Str(""), "wait_for_issuance": tftypes.NewValue(tftypes.Bool, wait),
		"common_name": UnknownStr(), "status": UnknownStr(), "pending_reason": UnknownStr(), "failure_code": UnknownStr(), "validation_records": Unknown(recordType),
		"issuer": UnknownStr(), "serial_number": UnknownStr(), "fingerprint_sha256": UnknownStr(), "not_before": UnknownStr(), "not_after": UnknownStr(), "renew_after": UnknownStr(),
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

func records(t *testing.T, m model) []validationRecord {
	t.Helper()
	var out []validationRecord
	if d := m.ValidationRecords.ElementsAs(Ctx, &out, false); d.HasError() {
		t.Fatal(d)
	}
	return out
}

func create(t *testing.T, api *fakeAPI, wait bool) *resource.CreateResponse {
	t.Helper()
	resp := &resource.CreateResponse{State: EmptyState(testSchema(t))}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(t, wait)}, resp)
	return resp
}

func TestCreateWaitsForIssuance(t *testing.T) {
	api := newFake(client.CertificatePendingValidation, client.CertificateIssuing, client.CertificateIssued)
	resp := create(t, api, true)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 0 {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Status.ValueString() != "issued" || m.SerialNumber.ValueString() != "0a1b" || m.CommonName.ValueString() != "example.com" || !api.lastReq.Exportable {
		t.Fatalf("model = %+v", m)
	}
	// The two names that share a challenge name share one CNAME record.
	if recs := records(t, m); len(recs) != 1 || recs[0].Name.ValueString() != "_acme-challenge.api.example.org" || recs[0].Type.ValueString() != "CNAME" {
		t.Fatalf("records = %+v", recs)
	}
}

// Without waiting, create returns at once with the CNAMEs to add.
func TestCreateWithoutWaitingReturnsTheRecords(t *testing.T) {
	api := newFake()
	resp := create(t, api, false)
	m := getModel(t, resp.State)
	if resp.Diagnostics.HasError() || api.reads != 0 || m.Status.ValueString() != "pending_validation" || len(records(t, m)) != 1 {
		t.Fatalf("diagnostics = %v, reads = %d, model = %+v", resp.Diagnostics, api.reads, m)
	}
}

// A certificate still validating when the wait ends is kept with a warning,
// not an error: an error would taint it and the next apply would request a
// new one.
func TestCreateTimeoutWarnsWithTheCNAMEs(t *testing.T) {
	api := newFake(client.CertificatePendingValidation, client.CertificatePendingValidation, client.CertificatePendingValidation, client.CertificatePendingValidation)
	resp := create(t, api, true)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if w := resp.Diagnostics.Warnings()[0].Detail(); !strings.Contains(w, "_acme-challenge.api.example.org CNAME k3x.acme.example.net") || !strings.Contains(w, "wait_for_issuance = false") {
		t.Fatalf("warning = %s", w)
	}
}

func TestCreateOfAFailedCertificateIsAnError(t *testing.T) {
	api := newFake(client.CertificateFailed)
	api.failure = "caa_forbidden"
	resp := create(t, api, true)
	if !strings.Contains(ErrorText(resp.Diagnostics), "caa_forbidden") || !strings.Contains(ErrorText(resp.Diagnostics), "CAA record") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
	if IsRemoved(resp.State) {
		t.Fatal("the failed certificate must stay in state, to be replaced")
	}
}

func TestDeleteInUseNamesTheUser(t *testing.T) {
	api := newFake()
	st := create(t, api, false).State
	c := api.certs["cert_1"]
	c.InUseBy = []client.CertificateUsage{{Type: "load_balancer", ID: "lb_1"}}
	api.certs["cert_1"] = c
	api.deleteErr = &client.APIError{Status: 409, Code: client.CodeCertificateInUse}
	resp := &resource.DeleteResponse{State: st}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
	if errs := resp.Diagnostics.Errors(); len(errs) != 1 || errs[0].Summary() != "Certificate in use" || !strings.Contains(errs[0].Detail(), "load balancer lb_1") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	api.deleteErr = nil
	for range 2 { // gone the second time: success
		resp = &resource.DeleteResponse{State: st}
		(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
	}
}

func TestReadAndImport(t *testing.T) {
	api := newFake()
	st := create(t, api, false).State
	delete(api.certs, "cert_1")
	resp := &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if !IsRemoved(resp.State) {
		t.Fatal("a deleted certificate must leave state")
	}
	api.certs["cert_9"] = client.Certificate{ID: "cert_9", Kind: client.CertificateKindImported}
	s := testSchema(t)
	for id, want := range map[string]string{"cert_9": "pantechdynamics_certificate_import", "dnsz_1": "Invalid certificate id"} {
		iresp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{api: api}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, iresp)
		if !strings.Contains(ErrorText(iresp.Diagnostics), want) {
			t.Errorf("%s: errors = %q", id, ErrorText(iresp.Diagnostics))
		}
	}
}

func TestNamesValidator(t *testing.T) {
	ok := func(vals ...string) types.List {
		if vals == nil {
			vals = []string{} // an empty list, not a null one
		}
		l, _ := types.ListValueFrom(Ctx, types.StringType, vals)
		return l
	}
	tests := []struct {
		names   types.List
		wantErr string
	}{
		{ok("example.com", "*.example.com", "xn--bcher-kva.example"), ""},
		{ok("Example.com"), "lower case"},
		{ok("api.*.example.com"), "left-most label"},
		{ok("**.example.com"), "left-most label"},
		{ok("example.com", "example.com"), "more than once"},
		{ok("localhost"), "two labels"},
		{ok(), "1 to 100"},
	}
	for _, tt := range tests {
		var r validator.ListResponse
		CertificateNames().ValidateList(Ctx, validator.ListRequest{Path: path.Root("names"), ConfigValue: tt.names}, &r)
		got := ErrorText(r.Diagnostics)
		if (tt.wantErr == "") != (got == "") || !strings.Contains(got, tt.wantErr) {
			t.Errorf("%v: errors = %q, want %q", tt.names, got, tt.wantErr)
		}
	}
}
