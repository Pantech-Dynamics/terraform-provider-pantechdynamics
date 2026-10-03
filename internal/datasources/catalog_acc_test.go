package datasources_test

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

// Acceptance tests run only with TF_ACC=1, against the dev API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY. The catalog data
// sources are read-only, so nothing is created and nothing needs cleaning up.
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func preCheck(t *testing.T) {
	t.Helper()
	for _, env := range []string{"PANTECHDYNAMICS_BASE_URL", "PANTECHDYNAMICS_API_KEY"} {
		if os.Getenv(env) == "" {
			t.Fatalf("%s must be set for acceptance tests", env)
		}
	}
}

// checkListHasValue passes when some element of a computed list of objects has
// attr == want, for example plans.N.slug == "developer".
func checkListHasValue(addr, list, attr, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		n, err := strconv.Atoi(rs.Primary.Attributes[list+".#"])
		if err != nil || n == 0 {
			return fmt.Errorf("%s.%s is empty or missing", addr, list)
		}
		for i := 0; i < n; i++ {
			if rs.Primary.Attributes[fmt.Sprintf("%s.%d.%s", list, i, attr)] == want {
				return nil
			}
		}
		return fmt.Errorf("no element of %s.%s has %s = %q", addr, list, attr, want)
	}
}

func TestAccPlansDataSource(t *testing.T) {
	const addr = "data.pantechdynamics_plans.all"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			// First, because the final destroy step reuses the last config, which
			// must therefore be a valid one.
			{
				Config:      `data "pantechdynamics_plans" "all" { placement = "bogus" }`,
				ExpectError: regexp.MustCompile(`Expected one of standard, vpc`),
			},
			{
				Config: `data "pantechdynamics_plans" "all" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "plans"),
					checkListHasValue(addr, "plans", "slug", "developer"),
					resource.TestCheckResourceAttrSet(addr, "plans.0.vcpu"),
					resource.TestCheckResourceAttrSet(addr, "plans.0.monthly_estimate_minor"),
				),
			},
			{
				Config: `data "pantechdynamics_plans" "all" { placement = "vpc" }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "placement", "vpc"),
					checkListHasValue(addr, "plans", "slug", "developer"),
				),
			},
		},
	})
}

func TestAccImagesDataSource(t *testing.T) {
	const addr = "data.pantechdynamics_images.all"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config: `data "pantechdynamics_images" "all" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "images"),
					checkListHasValue(addr, "images", "slug", "ubuntu-24-04"),
					resource.TestCheckResourceAttrSet(addr, "images.0.zones.#"),
				),
			},
		},
	})
}

func TestAccRegionsDataSource(t *testing.T) {
	const addr = "data.pantechdynamics_regions.all"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config: `data "pantechdynamics_regions" "all" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "regions"),
					checkListHasValue(addr, "regions", "code", "af-abj"),
					resource.TestCheckResourceAttrSet(addr, "regions.0.placements.0.kind"),
					resource.TestCheckResourceAttrSet(addr, "regions.0.placements.0.zone"),
				),
			},
		},
	})
}
