package certificateimport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
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

// selfSigned makes a certificate and its PKCS #8 key, valid until notAfter.
func selfSigned(t *testing.T, name string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: notAfter.Add(-48 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
}

// fakeAPI is an in-memory importAPI; it reports the fingerprint of what it
// holds, as the API does.
type fakeAPI struct {
	certs     map[string]client.Certificate
	reimports int
	lastPEM   string
	reimpErr  error
}

func (f *fakeAPI) record(id, certPEM string) client.Certificate {
	fp, _ := Fingerprint(certPEM)
	c := client.Certificate{ID: id, Kind: client.CertificateKindImported, Names: []string{"example.com"}, KeyType: "ecdsa-p256", Status: client.CertificateIssued, FingerprintSHA256: &fp}
	f.certs[id] = c
	return c
}

func (f *fakeAPI) ImportCertificate(_ context.Context, req client.ImportCertificateRequest) (*client.Certificate, error) {
	f.lastPEM = req.CertificatePEM
	c := f.record("cert_1", req.CertificatePEM)
	c.Exportable = req.Exportable
	f.certs["cert_1"] = c
	return &c, nil
}

func (f *fakeAPI) ReimportCertificate(_ context.Context, id string, req client.ReimportCertificateRequest) (*client.Certificate, error) {
	f.reimports++
	if f.reimpErr != nil {
		return nil, f.reimpErr
	}
	f.lastPEM = req.CertificatePEM
	c := f.record(id, req.CertificatePEM)
	return &c, nil
}

func (f *fakeAPI) GetCertificate(_ context.Context, id string) (*client.Certificate, error) {
	c, ok := f.certs[id]
	if !ok {
		return nil, fmt.Errorf("get: %w", &client.APIError{Status: 404, Code: client.CodeCertificateNotFound})
	}
	return &c, nil
}

func (f *fakeAPI) UpdateCertificateDescription(_ context.Context, id, d string) (*client.Certificate, error) {
	c := f.certs[id]
	c.Description = d
	f.certs[id] = c
	return &c, nil
}

func (f *fakeAPI) DeleteCertificate(_ context.Context, id string) error {
	delete(f.certs, id)
	return nil
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func planFor(t *testing.T, id tftypes.Value, certPEM, keyPEM string) tfsdk.Plan {
	s := testSchema(t)
	vals := map[string]tftypes.Value{
		"id": id, "certificate_pem": Str(certPEM), "chain_pem": Str(""), "private_key_pem": Str(keyPEM), "exportable": tftypes.NewValue(tftypes.Bool, false), "description": Str(""),
		"names": Unknown(tftypes.List{ElementType: tftypes.String}),
	}
	for _, a := range []string{"common_name", "key_type", "status", "issuer", "serial_number", "fingerprint_sha256", "not_before", "not_after"} {
		vals[a] = UnknownStr()
	}
	return Plan(s, vals)
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func TestImportReimportAndDrift(t *testing.T) {
	api := &fakeAPI{certs: map[string]client.Certificate{}}
	r := &Resource{api: api, now: time.Now}
	certA, keyA := selfSigned(t, "example.com", time.Now().Add(90*24*time.Hour))
	cresp := &resource.CreateResponse{State: EmptyState(testSchema(t))}
	r.Create(Ctx, resource.CreateRequest{Plan: planFor(t, UnknownStr(), certA, keyA)}, cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	m := getModel(t, cresp.State)
	if m.PrivateKeyPEM.ValueString() != keyA || m.Status.ValueString() != "issued" || len(m.Names.Elements()) != 1 {
		t.Fatalf("model = %+v", m)
	}

	// Re-imported outside Terraform: Read clears certificate_pem, so the
	// next plan sets the configured one back.
	certB, _ := selfSigned(t, "example.com", time.Now().Add(90*24*time.Hour))
	api.record("cert_1", certB)
	rresp := &resource.ReadResponse{State: cresp.State}
	r.Read(Ctx, resource.ReadRequest{State: cresp.State}, rresp)
	if getModel(t, rresp.State).CertificatePEM.ValueString() != "" {
		t.Fatal("drift must clear certificate_pem")
	}
	uresp := &resource.UpdateResponse{State: rresp.State}
	r.Update(Ctx, resource.UpdateRequest{Plan: planFor(t, Str("cert_1"), certA, keyA), State: rresp.State}, uresp)
	if uresp.Diagnostics.HasError() || api.reimports != 1 || api.lastPEM != certA {
		t.Fatalf("diagnostics = %v, reimports = %d", uresp.Diagnostics, api.reimports)
	}

	// A refused reimport for other names is explained.
	api.reimpErr = &client.APIError{Status: 422, Code: client.CodeCertificateNamesMismatch}
	certC, keyC := selfSigned(t, "other.example", time.Now().Add(90*24*time.Hour))
	uresp = &resource.UpdateResponse{State: cresp.State}
	r.Update(Ctx, resource.UpdateRequest{Plan: planFor(t, Str("cert_1"), certC, keyC), State: cresp.State}, uresp)
	if !strings.Contains(ErrorText(uresp.Diagnostics), "-replace") {
		t.Fatalf("diagnostics = %q", ErrorText(uresp.Diagnostics))
	}
}

// After `terraform import`, the PEMs are unknown to state; the configured
// certificate is recognised by its fingerprint and not sent again.
func TestUpdateAfterImportSkipsAnIdenticalReimport(t *testing.T) {
	api := &fakeAPI{certs: map[string]client.Certificate{}}
	certA, keyA := selfSigned(t, "example.com", time.Now().Add(90*24*time.Hour))
	api.record("cert_1", certA)
	r := &Resource{api: api, now: time.Now}
	s := testSchema(t)
	imported := State(s, map[string]tftypes.Value{"id": Str("cert_1")})
	rresp := &resource.ReadResponse{State: imported}
	r.Read(Ctx, resource.ReadRequest{State: imported}, rresp)
	uresp := &resource.UpdateResponse{State: rresp.State}
	r.Update(Ctx, resource.UpdateRequest{Plan: planFor(t, Str("cert_1"), certA, keyA), State: rresp.State}, uresp)
	if uresp.Diagnostics.HasError() || api.reimports != 0 || getModel(t, uresp.State).PrivateKeyPEM.ValueString() != keyA {
		t.Fatalf("diagnostics = %v, reimports = %d", uresp.Diagnostics, api.reimports)
	}
}

func TestProblems(t *testing.T) {
	now := time.Now()
	certA, keyA := selfSigned(t, "example.com", now.Add(24*time.Hour))
	_, keyB := selfSigned(t, "example.com", now.Add(24*time.Hour))
	expired, keyE := selfSigned(t, "example.com", now.Add(-time.Hour))
	tests := []struct {
		name                 string
		cert, chain, key     string
		wantAttr, wantSubstr string
	}{
		{"valid", certA, "", keyA, "", ""},
		{"valid with chain", certA, certA, keyA, "", ""},
		{"wrong key", certA, "", keyB, "private_key_pem", "does not belong"},
		{"expired", expired, "", keyE, "certificate_pem", "expired"},
		{"not pem", "hello", "", keyA, "certificate_pem", "no PEM"},
		{"key in chain", certA, keyA, keyA, "chain_pem", "PRIVATE KEY block"},
		{"encrypted key", certA, "", "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n", "private_key_pem", "encrypted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := Problems(tt.cert, tt.chain, tt.key, now)
			if tt.wantAttr == "" {
				if len(problems) != 0 {
					t.Fatalf("problems = %v", problems)
				}
				return
			}
			if !strings.Contains(problems[tt.wantAttr], tt.wantSubstr) {
				t.Fatalf("problems = %v, want %s: %s", problems, tt.wantAttr, tt.wantSubstr)
			}
		})
	}
}

func TestImportStateRefusesAPublicCertificate(t *testing.T) {
	api := &fakeAPI{certs: map[string]client.Certificate{"cert_2": {ID: "cert_2", Kind: client.CertificateKindPublic}}}
	resp := &resource.ImportStateResponse{State: EmptyState(testSchema(t))}
	(&Resource{api: api}).ImportState(Ctx, resource.ImportStateRequest{ID: "cert_2"}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "pantechdynamics_certificate instead") {
		t.Fatalf("errors = %q", ErrorText(resp.Diagnostics))
	}
}
