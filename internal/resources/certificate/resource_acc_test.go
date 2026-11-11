package certificate_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/echoprovider"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

const (
	certAddr   = "pantechdynamics_certificate.test"
	importAddr = "pantechdynamics_certificate_import.test"
	exportAddr = "data.pantechdynamics_certificate_export.test"
)

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY: an owner's or admin's
// key with the delete scope (export and delete need it). Certificates are
// free, but at most 20 are requested or imported a day per organization.
//   - The import test needs nothing else: it imports a self-signed
//     certificate it makes, for a tfacc-* name.
//   - The request test needs TF_ACC_CERT_ZONE, the name of an active Pantech
//     DNS zone of the organization, so the CA can validate without CNAMEs.
//     It requests one exportable certificate, waits for it, exports it once
//     (every owner and admin is emailed) and deletes it.
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func requireAcc(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
}

func preCheck(t *testing.T) {
	t.Helper()
	for _, env := range []string{"PANTECHDYNAMICS_BASE_URL", "PANTECHDYNAMICS_API_KEY"} {
		if os.Getenv(env) == "" {
			t.Fatalf("%s must be set for acceptance tests", env)
		}
	}
}

func apiClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(os.Getenv("PANTECHDYNAMICS_BASE_URL"), os.Getenv("PANTECHDYNAMICS_API_KEY"), "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sweep deletes the test's certificates if a failed run left them.
func sweep(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		c := apiClient(t)
		certs, err := c.ListCertificates(context.Background(), name)
		if err != nil {
			t.Logf("sweep: listing certificates: %v", err)
			return
		}
		for _, cert := range certs {
			if strings.Contains(strings.Join(cert.Names, ","), name) {
				if err := c.DeleteCertificate(context.Background(), cert.ID); err != nil && !errors.Is(err, client.ErrNotFound) {
					t.Logf("sweep: deleting %s: %v", cert.ID, err)
				}
			}
		}
	})
}

func checkGone(t *testing.T, name string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		certs, err := apiClient(t).ListCertificates(context.Background(), name)
		if err != nil {
			return err
		}
		for _, cert := range certs {
			if strings.Contains(strings.Join(cert.Names, ","), name) {
				return fmt.Errorf("certificate %s still exists after destroy", cert.ID)
			}
		}
		return nil
	}
}

// selfSigned makes a certificate and key for name, as PEM.
func selfSigned(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour)}
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

func importConfig(certPEM, keyPEM, description string) string {
	return fmt.Sprintf(`
resource "pantechdynamics_certificate_import" "test" {
  certificate_pem = %q
  private_key_pem = %q
  exportable      = true
  description     = %q
}

data "pantechdynamics_certificate" "by_id" {
  id = pantechdynamics_certificate_import.test.id
}
`, certPEM, keyPEM, description)
}

// Import, a clean re-plan, an in-place re-import with a new certificate for
// the same name, a description change, import of the resource, and destroy.
func TestAccCertificate_import(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix("tfacc") + ".example.com"
	sweep(t, name)
	certA, keyA := selfSigned(t, name)
	certB, keyB := selfSigned(t, name)
	var fingerprint string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkGone(t, name),
		Steps: []resource.TestStep{
			{
				Config: importConfig(certA, keyA, "tfacc"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(importAddr, "id", regexp.MustCompile(`^cert_`)),
					resource.TestCheckResourceAttr(importAddr, "names.0", name),
					resource.TestCheckResourceAttr(importAddr, "status", "issued"),
					resource.TestCheckResourceAttrPair("data.pantechdynamics_certificate.by_id", "fingerprint_sha256", importAddr, "fingerprint_sha256"),
					func(s *terraform.State) error {
						fingerprint = s.RootModule().Resources[importAddr].Primary.Attributes["fingerprint_sha256"]
						return nil
					},
				),
			},
			{Config: importConfig(certA, keyA, "tfacc"), PlanOnly: true},
			{
				Config: importConfig(certB, keyB, "tfacc v2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(importAddr, "description", "tfacc v2"),
					func(s *terraform.State) error {
						if got := s.RootModule().Resources[importAddr].Primary.Attributes["fingerprint_sha256"]; got == fingerprint {
							return errors.New("the certificate was not re-imported")
						}
						return nil
					},
				),
			},
			{
				ResourceName: importAddr, ImportState: true, ImportStateVerify: true,
				// The API never returns the PEMs.
				ImportStateVerifyIgnore: []string{"certificate_pem", "chain_pem", "private_key_pem"},
			},
			// After the import step the configured PEMs match by fingerprint:
			// nothing is re-imported, the plan is clean.
			{Config: importConfig(certB, keyB, "tfacc v2"), PlanOnly: true},
		},
	})
}

// A real request, validated through a Pantech DNS zone, then exported once.
func TestAccCertificate_request(t *testing.T) {
	requireAcc(t)
	zone := os.Getenv("TF_ACC_CERT_ZONE")
	if zone == "" {
		t.Skip("set TF_ACC_CERT_ZONE to an active Pantech DNS zone of the organization to request a real certificate")
	}
	name := acctest.RandomWithPrefix("tfacc") + "." + zone
	sweep(t, name)
	config := fmt.Sprintf(`
resource "pantechdynamics_certificate" "test" {
  names      = [%q]
  exportable = true
}

data "pantechdynamics_certificate_export" "test" {
  certificate_id = pantechdynamics_certificate.test.id
}
`, name)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkGone(t, name),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(certAddr, "status", "issued"),
					resource.TestCheckResourceAttr(certAddr, "validation_records.#", "0"),
					resource.TestCheckResourceAttrSet(certAddr, "serial_number"),
					resource.TestMatchResourceAttr(exportAddr, "full_chain_pem", regexp.MustCompile(`BEGIN CERTIFICATE`)),
					resource.TestCheckResourceAttrSet(exportAddr, "private_key_pem"),
				),
			},
			{ResourceName: certAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
		},
	})
}

// The ephemeral export, the recommended way: the key reaches the echo test
// provider (which stands in for whatever consumes it) but never this
// provider's state. It imports an exportable self-signed certificate, so it
// needs no DNS zone, and exports it once per plan and apply (each emails the
// organization's owners and admins).
func TestAccCertificate_ephemeralExport(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix("tfacc") + ".example.com"
	sweep(t, name)
	certPEM, keyPEM := selfSigned(t, name)
	config := importConfig(certPEM, keyPEM, "tfacc") + `
ephemeral "pantechdynamics_certificate_export" "test" {
  certificate_id = pantechdynamics_certificate_import.test.id
}

provider "echo" {
  data = ephemeral.pantechdynamics_certificate_export.test
}

resource "echo" "test" {}
`
	factories := map[string]func() (tfprotov6.ProviderServer, error){
		"pantechdynamics": protoV6Factories["pantechdynamics"],
		"echo":            echoprovider.NewProviderServer(),
	}
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkGone(t, name),
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestMatchResourceAttr("echo.test", "data.private_key_pem", regexp.MustCompile(`BEGIN PRIVATE KEY`)),
				resource.TestMatchResourceAttr("echo.test", "data.full_chain_pem", regexp.MustCompile(`BEGIN CERTIFICATE`)),
				// The key is nowhere in this provider's state.
				func(s *terraform.State) error {
					exported := s.RootModule().Resources["echo.test"].Primary.Attributes["data.private_key_pem"]
					for addr, rs := range s.RootModule().Resources {
						if strings.HasPrefix(addr, "echo.") || exported == "" {
							continue
						}
						for k, v := range rs.Primary.Attributes {
							// The imported key is configuration; only the
							// exported value must never be stored.
							if v == exported && (addr != importAddr || k != "private_key_pem") {
								return fmt.Errorf("%s.%s holds the exported key", addr, k)
							}
						}
					}
					return nil
				},
			),
		}},
	})
}

// Zero-cost checks: everything refused at plan time.
func TestAccCertificate_validation(t *testing.T) {
	requireAcc(t)
	certA, keyA := selfSigned(t, "tfacc.example.com")
	_, keyB := selfSigned(t, "tfacc.example.com")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{Config: `resource "pantechdynamics_certificate" "test" { names = ["Example.com"] }`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid certificate name`)},
			{Config: `resource "pantechdynamics_certificate" "test" { names = ["api.*.example.com"] }`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid certificate name`)},
			{Config: `resource "pantechdynamics_certificate" "test" {
  names    = ["example.com"]
  key_type = "rsa-4096"
}`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid value`)},
			{Config: importConfig(certA, keyB, ""), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid certificate`)},
			{Config: importConfig("not a certificate", keyA, ""), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid certificate`)},
		},
	})
}
