package dnsrecord_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

const (
	zoneAddr   = "pantechdynamics_dns_zone.test"
	recordAddr = "pantechdynamics_dns_record.www"
	mxAddr     = "pantechdynamics_dns_record.mx"
)

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY (an owner's or admin's
// key with the write and delete scopes). Zones and records are free, but an
// organization may create only 10 zones in 24 hours, so one test creates one
// zone and runs every check against it. The zone is
// tfacc-<random>.<TF_ACC_DNS_ZONE_PARENT> (default example.com): it is never
// delegated, so it stays awaiting_delegation and serves nothing, and its
// records stay pending (each create and update ends with a warning saying
// so). Each run
// deletes its zone, and a sweep removes any a failed run left behind.
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// requireAcc skips the test unless TF_ACC is set. It is the first line of every
// acceptance test, because the helpers call the live API before resource.Test
// would skip.
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

func zoneName() string {
	parent := os.Getenv("TF_ACC_DNS_ZONE_PARENT")
	if parent == "" {
		parent = "example.com"
	}
	return acctest.RandomWithPrefix("tfacc") + "." + parent
}

// sweep deletes the test's zone if a failed run left it behind.
func sweep(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		c := apiClient(t)
		zones, err := c.ListDNSZones(context.Background(), name)
		if err != nil {
			t.Logf("sweep: listing zones: %v", err)
			return
		}
		for _, z := range zones {
			if z.Name == name && z.Status != client.DNSZoneDeleting {
				if _, err := c.DeleteDNSZone(context.Background(), z.ID); err != nil && !errors.Is(err, client.ErrNotFound) {
					t.Logf("sweep: deleting %s: %v", z.ID, err)
				}
			}
		}
	})
}

// config is the zone, an A record set written in mixed case, and an MX record
// set with a host name in its non-canonical form.
func config(zone, ttl string, addresses ...string) string {
	quoted := make([]string, len(addresses))
	for i, a := range addresses {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	return fmt.Sprintf(`
resource "pantechdynamics_dns_zone" "test" {
  name        = %q
  description = "tfacc"
}

resource "pantechdynamics_dns_record" "www" {
  zone_id = pantechdynamics_dns_zone.test.id
  name    = "WWW"
  type    = "A"
  ttl     = %s
  values  = [%s]
}

resource "pantechdynamics_dns_record" "mx" {
  zone_id = pantechdynamics_dns_zone.test.id
  name    = "@"
  type    = "MX"
  values  = ["10 Mail.%s"]
}

data "pantechdynamics_dns_zone" "by_name" {
  name       = pantechdynamics_dns_zone.test.name
  depends_on = [pantechdynamics_dns_zone.test]
}

data "pantechdynamics_dns_records" "a" {
  zone_id    = pantechdynamics_dns_zone.test.id
  type       = "A"
  depends_on = [pantechdynamics_dns_record.www]
}
`, zone, ttl, strings.Join(quoted, ", "), zone)
}

func checkZoneDestroyed(t *testing.T, name string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		zones, err := apiClient(t).ListDNSZones(context.Background(), name)
		if err != nil {
			return err
		}
		for _, z := range zones {
			if z.Name == name && z.Status != client.DNSZoneDeleted && z.Status != client.DNSZoneDeleting && z.Status != client.DNSZoneFailed {
				return fmt.Errorf("zone %s (%s) still exists after destroy", z.ID, z.Status)
			}
		}
		return nil
	}
}

func attr(s *terraform.State, addr, key string) (string, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return "", fmt.Errorf("%s not in state", addr)
	}
	return rs.Primary.Attributes[key], nil
}

// Zone and records end to end: create, a clean re-plan despite non-canonical
// spelling, an in-place update, both import forms, the data sources, drift,
// and destroy.
func TestAccDNS_zoneAndRecords(t *testing.T) {
	requireAcc(t)
	zone := zoneName()
	sweep(t, zone)
	var recordID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkZoneDestroyed(t, zone),
		Steps: []resource.TestStep{
			{
				Config: config(zone, "300", "203.0.113.10"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(zoneAddr, "id", regexp.MustCompile(`^dnsz_`)),
					resource.TestCheckResourceAttr(zoneAddr, "status", client.DNSZoneAwaitingDelegation),
					resource.TestCheckResourceAttr(zoneAddr, "name_servers.#", "2"),
					resource.TestMatchResourceAttr(recordAddr, "id", regexp.MustCompile(`^dnsr_`)),
					resource.TestCheckResourceAttr(recordAddr, "status", client.DNSRecordPending),
					resource.TestCheckResourceAttr(recordAddr, "fqdn", "www."+zone),
					resource.TestCheckResourceAttr(recordAddr, "name", "WWW"),
					resource.TestCheckResourceAttr(mxAddr, "values.0", "10 Mail."+zone),
					resource.TestCheckResourceAttrPair("data.pantechdynamics_dns_zone.by_name", "id", zoneAddr, "id"),
					resource.TestCheckResourceAttr("data.pantechdynamics_dns_records.a", "records.#", "1"),
					resource.TestCheckResourceAttr("data.pantechdynamics_dns_records.a", "records.0.name", "www"),
					func(s *terraform.State) error {
						id, err := attr(s, recordAddr, "id")
						recordID = id
						return err
					},
				),
			},
			{
				// The backend stores "www" and "10 mail.<zone>."; the
				// configuration's spelling must not show as a change.
				Config:   config(zone, "300", "203.0.113.10"),
				PlanOnly: true,
			},
			{
				Config: config(zone, "600", "203.0.113.10", "203.0.113.11"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(recordAddr, "ttl", "600"),
					resource.TestCheckResourceAttr(recordAddr, "values.#", "2"),
					resource.TestCheckResourceAttr(recordAddr, "version", "2"),
					resource.TestCheckResourceAttr(recordAddr, "status", client.DNSRecordPending),
				),
			},
			{
				ResourceName:            recordAddr,
				ImportState:             true,
				ImportStateIdFunc:       importID(recordAddr, false),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name", "timeouts"}, // the import holds the stored "www"
			},
			{
				ResourceName:            mxAddr,
				ImportState:             true,
				ImportStateIdFunc:       importID(mxAddr, true),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"values", "timeouts"}, // the import holds the stored "10 mail.<zone>."
			},
			{
				ResourceName:            zoneAddr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "record_count", "status"},
			},
			{
				// Delete the A record set behind Terraform's back: Read drops it,
				// and the plan wants it back.
				PreConfig: func() {
					c := apiClient(t)
					zones, err := c.ListDNSZones(context.Background(), zone)
					if err != nil || len(zones) == 0 {
						t.Fatalf("finding zone: %v", err)
					}
					if err := c.DeleteDNSRecord(context.Background(), zones[0].ID, recordID); err != nil {
						t.Fatalf("deleting record out of band: %v", err)
					}
					if err := c.WaitUntil(context.Background(), "record gone", func(ctx context.Context) (bool, error) {
						_, err := c.GetDNSRecord(ctx, zones[0].ID, recordID)
						return errors.Is(err, client.ErrNotFound), nil
					}); err != nil {
						t.Fatal(err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config(zone, "600", "203.0.113.10", "203.0.113.11"),
				Check:  resource.TestCheckResourceAttr(recordAddr, "status", client.DNSRecordPending),
			},
		},
	})
}

// importID builds "<zone_id>/<record_id>", or "<zone_id>/<name>/<type>".
func importID(addr string, byName bool) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		zoneID, err := attr(s, addr, "zone_id")
		if err != nil {
			return "", err
		}
		if byName {
			name, _ := attr(s, addr, "name")
			typ, _ := attr(s, addr, "type")
			return zoneID + "/" + name + "/" + typ, nil
		}
		id, err := attr(s, addr, "id")
		return zoneID + "/" + id, err
	}
}

// Zero-cost checks: plan-time validation and wrong import ids.
func TestAccDNS_validation(t *testing.T) {
	requireAcc(t)
	record := func(typ, name string, values ...string) string {
		quoted := make([]string, len(values))
		for i, v := range values {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		return fmt.Sprintf(`
resource "pantechdynamics_dns_record" "www" {
  zone_id = "dnsz_none"
  name    = %q
  type    = %q
  values  = [%s]
}
`, name, typ, strings.Join(quoted, ", "))
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{Config: `resource "pantechdynamics_dns_zone" "test" { name = "Example.com." }`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid dns zone name`)},
			{Config: record("A", "www", "999.1.1.1"), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid A value`)},
			{Config: record("A", "www", "10.0.0.5"), PlanOnly: true, ExpectError: regexp.MustCompile(`Private address`)},
			{Config: record("CNAME", "@", "target.example.com"), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid CNAME name`)},
			{Config: record("SOA", "@", "x"), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid value`)},
			{
				Config:        record("A", "www", "203.0.113.10"),
				ResourceName:  recordAddr,
				ImportState:   true,
				ImportStateId: "dnsr_1",
				ExpectError:   regexp.MustCompile(`Invalid import id`),
			},
		},
	})
}
