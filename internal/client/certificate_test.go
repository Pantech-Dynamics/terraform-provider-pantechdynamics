package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const certificateJSON = `{"id":"cert_1","kind":"public","names":["example.com","api.example.org"],"common_name":"example.com","description":"","key_type":"ecdsa-p256","exportable":false,"status":"pending_validation","pending_reason":"awaiting_cname","failure_code":null,
"validations":[{"name":"example.com","method":"pantech_dns","zone_id":"dnsz_1","cname_name":null,"cname_target":null,"status":"ready"},{"name":"api.example.org","method":"cname","zone_id":null,"cname_name":"_acme-challenge.api.example.org","cname_target":"k3x.acme.pantechdynamics.com","status":"pending"}],
"validation_deadline_at":"2026-11-13T10:00:00Z","issuer":null,"serial_number":null,"fingerprint_sha256":null,"not_before":null,"not_after":null,"issued_at":null,"renew_after":null,"renewal_status":null,"in_use_by":[],"revocation_pending":false,"revoked_at":null,"created_at":"2026-11-10T10:00:00Z","updated_at":"2026-11-10T10:00:00Z","version":1}`

func TestRequestCertificate(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"accepted", 202, certificateJSON, ""},
		{"limit", 403, `{"status":403,"code":"CERTIFICATE_LIMIT_REACHED"}`, "CERTIFICATE_LIMIT_REACHED"},
		{"bad name", 422, `{"status":422,"code":"VALIDATION_FAILED","errors":[{"field":"names[1]","code":"INVALID_NAME","message":"bad"}]}`, "VALIDATION_FAILED"},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, "UNAUTHENTICATED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/certificates" || string(body) != `{"names":["example.com","api.example.org"],"exportable":false}` || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s %s", r.Method, r.URL.Path, body)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			cert, err := c.RequestCertificate(context.Background(), RequestCertificateRequest{Names: []string{"example.com", "api.example.org"}})
			if tt.wantErr != "" {
				if !HasCode(err, tt.wantErr) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || cert.ID != "cert_1" || len(cert.Validations) != 2 || *cert.Validations[1].CNAMEName != "_acme-challenge.api.example.org" || cert.Validations[0].CNAMEName != nil {
				t.Fatalf("cert = %+v, err = %v", cert, err)
			}
		})
	}
}

func TestGetCertificateNotFoundAndMalformed(t *testing.T) {
	for body, wantNotFound := range map[string]bool{`{"status":404,"code":"CERTIFICATE_NOT_FOUND"}`: true, `{"id":`: false} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/certificates/cert_1" {
				t.Errorf("path = %s", r.URL.Path)
			}
			if strings.Contains(body, "404") {
				w.WriteHeader(404)
			}
			_, _ = w.Write([]byte(body))
		})
		_, err := c.GetCertificate(context.Background(), "cert_1")
		if err == nil || errors.Is(err, ErrNotFound) != wantNotFound {
			t.Errorf("body %s: err = %v", body, err)
		}
	}
}

// The import body carries the key; a gateway error is retried with the
// same key, because an import replays.
func TestImportCertificateRetriesWithTheSameKey(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/certificates/import" || !strings.Contains(string(body), `"private_key_pem":"KEY"`) || strings.Contains(string(body), "chain_pem") {
			t.Errorf("got %s %s", r.URL.Path, body)
		}
		if len(keys) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(strings.Replace(certificateJSON, `"kind":"public"`, `"kind":"imported"`, 1)))
	})
	cert, err := c.ImportCertificate(context.Background(), ImportCertificateRequest{CertificatePEM: "CERT", PrivateKeyPEM: "KEY"})
	if err != nil || cert.Kind != CertificateKindImported || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("cert = %+v, err = %v, keys = %v", cert, err, keys)
	}
}

func TestReimportExportDeleteAndUpdate(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/v1/certificates/cert_1/reimport":
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"status":422,"code":"CERTIFICATE_NAMES_MISMATCH"}`))
		case r.URL.Path == "/v1/certificates/cert_1/export":
			if r.Method != http.MethodPost || len(body) != 0 {
				t.Errorf("export got %s %s", r.Method, body)
			}
			_, _ = w.Write([]byte(`{"certificate_pem":"C","chain_pem":"CH","private_key_pem":"K","not_after":"2027-02-08T10:00:00Z"}`))
		case r.Method == http.MethodPatch:
			if string(body) != `{"description":"web"}` {
				t.Errorf("body = %s", body)
			}
			_, _ = w.Write([]byte(certificateJSON))
		case r.Method == http.MethodDelete:
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"status":409,"code":"CERTIFICATE_IN_USE"}`))
		}
	})
	ctx := context.Background()
	if _, err := c.ReimportCertificate(ctx, "cert_1", ReimportCertificateRequest{CertificatePEM: "C", PrivateKeyPEM: "K"}); !HasCode(err, CodeCertificateNamesMismatch) {
		t.Fatalf("reimport err = %v", err)
	}
	if out, err := c.ExportCertificate(ctx, "cert_1"); err != nil || out.PrivateKeyPEM != "K" || out.NotAfter == nil {
		t.Fatalf("export = %+v, err = %v", out, err)
	}
	if _, err := c.UpdateCertificateDescription(ctx, "cert_1", "web"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteCertificate(ctx, "cert_1"); !HasCode(err, CodeCertificateInUse) {
		t.Fatalf("delete err = %v", err)
	}
}

func TestListCertificatesSendsQ(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "example.com" {
			t.Errorf("q = %q", r.URL.Query().Get("q"))
		}
		_, _ = w.Write([]byte(`{"data":[` + certificateJSON + `],"next_cursor":null}`))
	})
	certs, err := c.ListCertificates(context.Background(), "example.com")
	if err != nil || len(certs) != 1 {
		t.Fatalf("certs = %+v, err = %v", certs, err)
	}
}
