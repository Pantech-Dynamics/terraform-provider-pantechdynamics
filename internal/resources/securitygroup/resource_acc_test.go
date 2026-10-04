package securitygroup_test

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

const addr = "pantechdynamics_security_group.test"

// Acceptance tests run only with TF_ACC=1, against the dev API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY. Every group they create
// is named tfacc-sg-* and removed again. They never touch other groups.
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

func apiClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(os.Getenv("PANTECHDYNAMICS_BASE_URL"), os.Getenv("PANTECHDYNAMICS_API_KEY"), "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func config(name, rules string) string {
	return fmt.Sprintf(`
resource "pantechdynamics_security_group" "test" {
  name = %q
  rules = [
%s
  ]
}
`, name, rules)
}

const (
	sshRule  = `    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "10.0.0.0/8" },`
	icmpRule = `    { direction = "ingress", protocol = "icmp", cidr = "0.0.0.0/0" },`
	dnsRule  = `    { direction = "ingress", protocol = "udp", port_range = "53", cidr = "10.0.0.0/8" },`
)

// checkDestroyed fails if a group with this name is still on the account.
func checkDestroyed(t *testing.T, name *string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		groups, err := apiClient(t).ListSecurityGroups(context.Background())
		if err != nil {
			return err
		}
		for _, g := range groups {
			if g.Name == *name {
				return fmt.Errorf("security group %s (%s) still exists after destroy", g.Name, g.ID)
			}
		}
		return nil
	}
}

func captureID(dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		*dst = rs.Primary.ID
		return nil
	}
}

func checkID(old *string, wantSame bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		if same := rs.Primary.ID == *old; same != wantSame {
			return fmt.Errorf("id %q (previous %q): same = %v, want %v", rs.Primary.ID, *old, same, wantSame)
		}
		return nil
	}
}

// deleteAndWait removes a group through the API and waits until it is gone.
func deleteAndWait(t *testing.T, id string) {
	t.Helper()
	c := apiClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if _, err := c.DeleteSecurityGroup(ctx, id); err != nil && !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("deleting %s: %v", id, err)
	}
	for {
		if _, err := c.GetSecurityGroup(ctx, id); errors.Is(err, client.ErrNotFound) {
			return
		} else if err != nil {
			t.Fatalf("waiting for %s to disappear: %v", id, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func TestAccSecurityGroup_lifecycle(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc-sg")
	renamed := name + "-renamed"
	var firstID, secondID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &renamed),
		Steps: []resource.TestStep{
			{
				// Rejected at plan time, before any API call. First, because the
				// final destroy step reuses the last config, which must be valid.
				Config:      config(name, ""),
				ExpectError: regexp.MustCompile(`needs at least one rule`),
			},
			{
				Config:      config(name, `    { direction = "ingress", protocol = "icmp", port_range = "22", cidr = "0.0.0.0/0" },`),
				ExpectError: regexp.MustCompile(`port_range must be omitted`),
			},
			{
				Config: config(name, sshRule+"\n"+icmpRule),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`^sg_`)),
					resource.TestCheckResourceAttr(addr, "observed_state", "active"),
					resource.TestCheckResourceAttr(addr, "rules.#", "2"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
					captureID(&firstID),
				),
			},
			{
				// Import by id, and the imported state must match what apply saved.
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				// Rules change in place: same id, one more rule.
				Config: config(name, sshRule+"\n"+icmpRule+"\n"+dnsRule),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "rules.#", "3"),
					checkID(&firstID, true),
				),
			},
			{
				// A second plan after the update must be empty (no spurious diff).
				Config:   config(name, sshRule+"\n"+icmpRule+"\n"+dnsRule),
				PlanOnly: true,
			},
			{
				// Reordering the rules is not a change.
				Config:   config(name, dnsRule+"\n"+icmpRule+"\n"+sshRule),
				PlanOnly: true,
			},
			{
				// A rename cannot be done in place: the group is replaced.
				Config: config(renamed, sshRule),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", renamed),
					resource.TestCheckResourceAttr(addr, "rules.#", "1"),
					captureID(&secondID),
					checkID(&firstID, false),
				),
			},
		},
	})
}

func TestAccSecurityGroup_driftWhenDeletedOutsideTerraform(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc-sg-drift")
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &name),
		Steps: []resource.TestStep{
			{
				Config: config(name, sshRule),
				Check:  captureID(&id),
			},
			{
				// Delete the group behind Terraform's back, then refresh: Read must
				// drop it from state, so the plan wants to create it again. The test
				// stops here: the backend keeps a deleted group's name reserved, so
				// recreating the same name would fail with a 500.
				PreConfig:          func() { deleteAndWait(t, id) },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccSecurityGroup_duplicateNameIsReported(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc-sg-dup")
	var seededID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &name),
		Steps: []resource.TestStep{
			{
				// Another group already has the name, created out of band. The
				// backend would answer a duplicate with a 45 second 500, so the
				// provider must catch it first.
				PreConfig: func() {
					c := apiClient(t)
					ref, err := c.CreateSecurityGroup(context.Background(), client.CreateSecurityGroupRequest{
						Name:  name,
						Rules: []client.SecurityGroupRule{{Direction: "ingress", Protocol: "icmp", CIDR: "0.0.0.0/0"}},
					})
					if err != nil {
						t.Fatalf("seeding group: %v", err)
					}
					seededID = ref.ResourceID
					if err := c.WaitForOperation(context.Background(), ref.OperationID, nil); err != nil {
						t.Fatalf("waiting for seeded group: %v", err)
					}
				},
				Config:      config(name, sshRule),
				ExpectError: regexp.MustCompile(`already exists`),
			},
			{
				// Remove the seeded group so the final destroy check passes.
				PreConfig: func() { deleteAndWait(t, seededID) },
				Config:    config(name+"-ok", icmpRule),
			},
		},
	})
}

func TestAccSecurityGroup_importRejectsWrongID(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc-sg-imp")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &name),
		Steps: []resource.TestStep{
			{Config: config(name, sshRule)},
			{
				ResourceName:  addr,
				ImportState:   true,
				ImportStateId: "sshk_wrong_prefix",
				ExpectError:   regexp.MustCompile(`Expected an id starting with "sg_"`),
			},
		},
	})
}
