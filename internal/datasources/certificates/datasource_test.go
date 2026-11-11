package certificates

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	certs   []client.Certificate
	exports int
	exprErr error
}

func (f *fakeAPI) GetCertificate(_ context.Context, id string) (*client.Certificate, error) {
	for i := range f.certs {
		if f.certs[i].ID == id {
			return &f.certs[i], nil
		}
	}
	return nil, fmt.Errorf("get: %w", &client.APIError{Status: 404, Code: client.CodeCertificateNotFound})
}

func (f *fakeAPI) ListCertificates(context.Context, string) ([]client.Certificate, error) {
	return f.certs, nil
}

func (f *fakeAPI) ExportCertificate(context.Context, string) (*client.ExportedCertificate, error) {
	f.exports++
	if f.exprErr != nil {
		return nil, f.exprErr
	}
	return &client.ExportedCertificate{CertificatePEM: "LEAF\n", ChainPEM: "CHAIN\n", PrivateKeyPEM: "KEY"}, nil
}

func ptr(s string) *string { return &s }

func fixture() *fakeAPI {
	return &fakeAPI{certs: []client.Certificate{
		{ID: "cert_1", Kind: "public", Names: []string{"example.com", "*.example.com"}, CommonName: ptr("example.com"), Status: "issued", Exportable: true},
		{ID: "cert_2", Kind: "public", Names: []string{"example.com"}, Status: "failed"},
		{ID: "cert_3", Kind: "imported", Names: []string{"shop.example.org"}, Status: "issued"},
		{ID: "cert_4", Kind: "imported", Names: []string{"shop.example.org"}, Status: "issued"},
	}}
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

func TestLookup(t *testing.T) {
	tests := []struct {
		set     map[string]string
		wantID  string
		wantErr string
	}{
		{map[string]string{"name": "*.example.com"}, "cert_1", ""},
		{map[string]string{"name": "example.com"}, "cert_1", ""}, // the failed one is skipped
		{map[string]string{"id": "cert_3"}, "cert_3", ""},
		{map[string]string{"name": "shop.example.org"}, "", "cert_3, cert_4"},
		{map[string]string{"name": "nope.example"}, "", "no usable certificate"},
		{map[string]string{"id": "cert_9"}, "", "No certificate has this id"},
		{map[string]string{}, "", "exactly one"},
	}
	for _, tt := range tests {
		resp := read(t, &DataSource{api: fixture()}, tt.set)
		if tt.wantErr != "" {
			if !strings.Contains(errText(resp), tt.wantErr) {
				t.Errorf("%v: errors = %s", tt.set, errText(resp))
			}
			continue
		}
		var m model
		resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
		if resp.Diagnostics.HasError() || m.ID.ValueString() != tt.wantID {
			t.Errorf("%v: diagnostics = %v, id = %s", tt.set, resp.Diagnostics, m.ID.ValueString())
		}
	}
}

func TestExport(t *testing.T) {
	api := fixture()
	resp := read(t, &ExportDataSource{api: api}, map[string]string{"certificate_id": "cert_1"})
	var m exportModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.PrivateKeyPEM.ValueString() != "KEY" || m.FullChainPEM.ValueString() != "LEAF\nCHAIN\n" {
		t.Fatalf("diagnostics = %v, model = %+v", resp.Diagnostics, m)
	}

	// Not exportable: refused before the export call, which emails owners.
	resp = read(t, &ExportDataSource{api: api}, map[string]string{"certificate_id": "cert_3"})
	if !strings.Contains(errText(resp), "Certificate not exportable") || api.exports != 1 {
		t.Fatalf("errors = %s, exports = %d", errText(resp), api.exports)
	}

	api.exprErr = &client.APIError{Status: 409, Code: client.CodeCertificateNotIssued}
	resp = read(t, &ExportDataSource{api: api}, map[string]string{"certificate_id": "cert_1"})
	if !strings.Contains(errText(resp), "not issued yet") {
		t.Fatalf("errors = %s", errText(resp))
	}
}

// open drives the ephemeral resource's Open with the given certificate id.
func open(t *testing.T, api exportAPI, id string) *ephemeral.OpenResponse {
	t.Helper()
	e := &ExportEphemeral{api: api}
	var sresp ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &sresp)
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
	vals["certificate_id"] = tftypes.NewValue(tftypes.String, id)
	raw := tftypes.NewValue(typ, vals)
	resp := &ephemeral.OpenResponse{Result: tfsdk.EphemeralResultData{Schema: sresp.Schema, Raw: raw}}
	e.Open(ctx, ephemeral.OpenRequest{Config: tfsdk.Config{Schema: sresp.Schema, Raw: raw}}, resp)
	return resp
}

func TestEphemeralExport(t *testing.T) {
	api := fixture()
	resp := open(t, api, "cert_1")
	var m exportModel
	resp.Diagnostics.Append(resp.Result.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.PrivateKeyPEM.ValueString() != "KEY" || m.FullChainPEM.ValueString() != "LEAF\nCHAIN\n" || api.exports != 1 {
		t.Fatalf("diagnostics = %v, model = %+v, exports = %d", resp.Diagnostics, m, api.exports)
	}
	// Not exportable: refused before the export call.
	resp = open(t, api, "cert_3")
	if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != "Certificate not exportable" || api.exports != 1 {
		t.Fatalf("diagnostics = %v, exports = %d", resp.Diagnostics, api.exports)
	}
}

// The ephemeral resource is the recommended way, and both docs say each
// export emails owners and admins.
func TestExportDescriptions(t *testing.T) {
	var eresp ephemeral.SchemaResponse
	NewExportEphemeral().Schema(ctx, ephemeral.SchemaRequest{}, &eresp)
	var dresp datasource.SchemaResponse
	NewExport().Schema(ctx, datasource.SchemaRequest{}, &dresp)
	if !strings.Contains(eresp.Schema.Description, "recommended") || !strings.Contains(dresp.Schema.Description, "Prefer the pantechdynamics_certificate_export ephemeral resource") {
		t.Error("the docs must recommend the ephemeral resource")
	}
	for _, d := range []string{eresp.Schema.Description, dresp.Schema.Description} {
		if !strings.Contains(d, "emails your organization's owners and admins") {
			t.Errorf("description must say exports email owners: %s", d)
		}
	}
}
