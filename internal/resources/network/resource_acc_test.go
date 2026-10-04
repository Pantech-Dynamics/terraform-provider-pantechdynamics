package network_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

const addr = "pantechdynamics_network.test"

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY.
//
// A network is billed monthly (about NGN 17,000 on staging), so a full run
// creates exactly one network, renames it once (a replacement, so two in all),
// and deletes both. Every network is named tfacc-net-*. The platform does not
// list networks in the public API, so cleanup checks the ids Terraform saw
// instead of sweeping by name.
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// requireAcc skips the test unless TF_ACC is set. It must be the first line of
// every acceptance test, so an ordinary `go test ./...` never reaches the API.
func requireAcc(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests (they spend real credit)")
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

func config(name, cidr string) string {
	return fmt.Sprintf(`
resource "pantechdynamics_network" "test" {
  name = %q
  cidr = %q
}
`, name, cidr)
}

// ids collects every network id the test's state held, so CheckDestroy can
// verify each one is gone, including a replaced one.
type ids struct{ seen []string }

func (i *ids) capture(s *terraform.State) error {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return fmt.Errorf("%s not in state", addr)
	}
	i.seen = append(i.seen, rs.Primary.ID)
	return nil
}

// checkDestroyed fails if a network the test created is still live. A network
// that failed to provision stays readable as failed with a deleted intent, which
// counts as gone.
func (i *ids) checkDestroyed(t *testing.T) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		c := apiClient(t)
		for _, id := range i.seen {
			n, err := c.GetNetwork(context.Background(), id)
			if errors.Is(err, client.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			failedAndGone := n.ObservedState == "failed" && n.DesiredState == "deleted"
			if n.ObservedState != "deleted" && !failedAndGone {
				return fmt.Errorf("network %s is still %s/%s after destroy", id, n.DesiredState, n.ObservedState)
			}
		}
		return nil
	}
}

func deleteAndWait(t *testing.T, id string) {
	t.Helper()
	c := apiClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := c.DeleteNetwork(ctx, id); err != nil && !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("deleting %s: %v", id, err)
	}
	for {
		n, err := c.GetNetwork(ctx, id)
		if errors.Is(err, client.ErrNotFound) || (err == nil && n.ObservedState == "deleted") {
			return
		}
		if err != nil {
			t.Fatalf("waiting for %s to disappear: %v", id, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// Plan-time validation orders nothing.
func TestAccNetwork_validationIsFree(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{Config: config("tfacc-net-x", "10.0.0.5/16"), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid CIDR`)},
			{Config: config("tfacc-net-x", "fd00::/64"), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid CIDR`)},
			{Config: config("", "10.0.0.0/16"), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid network name`)},
		},
	})
}

func TestAccNetwork_lifecycle(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix("tfacc-net")
	renamed := name + "-renamed"
	var seen ids

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             seen.checkDestroyed(t),
		Steps: []resource.TestStep{
			{
				Config: config(name, "10.77.0.0/16"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestCheckResourceAttr(addr, "cidr", "10.77.0.0/16"),
					resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`^net_`)),
					resource.TestCheckResourceAttr(addr, "observed_state", "active"),
					resource.TestCheckResourceAttrSet(addr, "zone"),
					resource.TestCheckResourceAttrSet(addr, "region"),
					seen.capture,
				),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				// A second plan must be empty: the computed region and zone do not drift.
				Config:   config(name, "10.77.0.0/16"),
				PlanOnly: true,
			},
			{
				// A network cannot be edited, so a rename replaces it.
				Config: config(renamed, "10.77.0.0/16"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", renamed),
					seen.capture,
				),
			},
		},
	})
}

func TestAccNetwork_driftWhenDeletedOutsideTerraform(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix("tfacc-net-drift")
	var seen ids

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             seen.checkDestroyed(t),
		Steps: []resource.TestStep{
			{
				Config: config(name, "10.78.0.0/16"),
				Check:  seen.capture,
			},
			{
				PreConfig: func() { deleteAndWait(t, seen.seen[0]) },
				Config:    config(name, "10.78.0.0/16"),
				// Refresh notices the network is gone and plans to create it again.
				ExpectNonEmptyPlan: true,
				PlanOnly:           true,
			},
			{
				// Applying recreates it, so destroy has something to clean up.
				Config: config(name, "10.78.0.0/16"),
				Check:  seen.capture,
			},
		},
	})
}
