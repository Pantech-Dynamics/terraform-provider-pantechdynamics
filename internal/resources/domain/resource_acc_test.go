package domain_test

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

const resourceAddr = "pantechdynamics_domain.test"

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY.
//
// Registering a domain spends real credit and CANNOT be undone: destroy only
// removes it from state. So the registration test runs only when
// TF_ACC_DOMAIN_NAME names a domain to buy (an available name in the cheapest
// TLD) and TF_ACC_DOMAIN_SPEND=1 confirms the spend. It orders exactly one
// domain, for one year, with auto_renew off so it lapses on its own. Report
// the order it placed. Everything else here is zero-cost.
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

func config(name string, acceptTerms bool, transferLock bool) string {
	return fmt.Sprintf(`
resource "pantechdynamics_domain" "test" {
  name          = %q
  years         = 1
  auto_renew    = false
  transfer_lock = %t
  accept_terms  = %t

  contact = {
    first_name    = "Test"
    last_name     = "Account"
    email         = "domains@example.com"
    phone         = "+234.8012345678"
    address_line1 = "1 Test Street"
    city          = "Lagos"
    country_code  = "NG"
  }
}
`, name, transferLock, acceptTerms)
}

// Zero-cost checks: everything refused at plan time, before an order exists.
func TestAccDomain_validation(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{Config: config("tfacc-never-bought.com", false, true), PlanOnly: true, ExpectError: regexp.MustCompile(`Terms not accepted`)},
			{Config: config("Not A Domain", true, true), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid domain name`)},
			{Config: strings.Replace(config("tfacc-never-bought.com.ng", true, true), "accept_terms  = true", "accept_terms  = true\n  privacy       = false", 1), PlanOnly: true, ExpectError: regexp.MustCompile(`Privacy is always on for .ng`)},
			{Config: config("tfacc-never-bought.com.ng", true, true), PlanOnly: true, ExpectError: regexp.MustCompile(`Missing state`)},
			{
				Config:        config("tfacc-never-bought.com", true, true),
				ResourceName:  resourceAddr,
				ImportState:   true,
				ImportStateId: "dnsz_wrong_prefix",
				ExpectError:   regexp.MustCompile(`Invalid domain id`),
			},
		},
	})
}

func TestAccDomainTLDsDataSource(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{{
			Config: `data "pantechdynamics_domain_tlds" "all" {}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.pantechdynamics_domain_tlds.all", "id", "domain_tlds"),
				resource.TestMatchResourceAttr("data.pantechdynamics_domain_tlds.all", "tlds.#", regexp.MustCompile(`^[1-9]`)),
			),
		}},
	})
}

// Registers TF_ACC_DOMAIN_NAME (this spends credit), re-plans clean, turns the
// transfer lock off in place, reads it with the data source, imports it, and
// "destroys" it, which only removes it from state.
func TestAccDomain_register(t *testing.T) {
	requireAcc(t)
	name := os.Getenv("TF_ACC_DOMAIN_NAME")
	if name == "" || os.Getenv("TF_ACC_DOMAIN_SPEND") != "1" {
		t.Skip("set TF_ACC_DOMAIN_NAME and TF_ACC_DOMAIN_SPEND=1 to buy a domain: it spends credit and cannot be undone")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             func(*terraform.State) error { return nil }, // the domain stays registered by design
		Steps: []resource.TestStep{
			{
				Config: config(name, true, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(resourceAddr, "id", regexp.MustCompile(`^dom_`)),
					resource.TestMatchResourceAttr(resourceAddr, "order_id", regexp.MustCompile(`^dor_`)),
					resource.TestCheckResourceAttr(resourceAddr, "status", "active"),
					resource.TestCheckResourceAttr(resourceAddr, "auto_renew", "false"),
					resource.TestCheckResourceAttr(resourceAddr, "name_server_mode", "pantech"),
					resource.TestCheckResourceAttrSet(resourceAddr, "expires_at"),
				),
			},
			{Config: config(name, true, true), PlanOnly: true},
			{
				Config: config(name, true, false) + fmt.Sprintf(`
data "pantechdynamics_domain" "by_name" {
  name       = %q
  depends_on = [pantechdynamics_domain.test]
}
`, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "transfer_lock", "false"),
					resource.TestCheckResourceAttrPair("data.pantechdynamics_domain.by_name", "id", resourceAddr, "id"),
				),
			},
			{
				ResourceName:            resourceAddr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"years", "accept_terms", "order_id", "contact", "timeouts"},
			},
		},
	})
}
