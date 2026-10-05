package provider_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

// This test builds the whole VPC chain: a network, a subnet, firewall rules, an
// instance in the subnet, and a public IP, then swaps the static NAT address for
// a port forward. IT SPENDS REAL CREDIT: one network, one instance on the
// cheapest vpc plan, and two public IP addresses over its run. Because of that
// it needs TF_ACC_VPC_CHAIN=1 on top of TF_ACC=1.
//
// Everything is named tfacc-vpc-*. The checks at the end read each id Terraform
// saw, because the public API does not list networks.

var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func requireVPCChain(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" || os.Getenv("TF_ACC_VPC_CHAIN") == "" {
		t.Skip("set TF_ACC=1 and TF_ACC_VPC_CHAIN=1 to run the VPC chain test (it orders an instance and public IPs)")
	}
}

func apiClient(t *testing.T) *client.Client {
	t.Helper()
	for _, env := range []string{"PANTECHDYNAMICS_BASE_URL", "PANTECHDYNAMICS_API_KEY"} {
		if os.Getenv(env) == "" {
			t.Fatalf("%s must be set for acceptance tests", env)
		}
	}
	c, err := client.New(os.Getenv("PANTECHDYNAMICS_BASE_URL"), os.Getenv("PANTECHDYNAMICS_API_KEY"), "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// cheapestVPCPlan returns the vpc-placement plan with the lowest upfront payment.
func cheapestVPCPlan(t *testing.T, c *client.Client) string {
	t.Helper()
	plans, err := c.ListPlans(context.Background(), "vpc")
	if err != nil {
		t.Fatal(err)
	}
	best := ""
	var bestPrice int64
	for _, p := range plans {
		if p.Price == nil {
			continue
		}
		if best == "" || p.Price.InitialPaymentMinor < bestPrice {
			best, bestPrice = p.Slug, p.Price.InitialPaymentMinor
		}
	}
	if best == "" {
		t.Fatal("no priced vpc plan available")
	}
	return best
}

const chainBase = `
resource "pantechdynamics_ssh_key" "test" {
  name = "%[1]s"
}

resource "pantechdynamics_network" "test" {
  name = "%[1]s"
  cidr = "10.79.0.0/16"
}

resource "pantechdynamics_subnet" "test" {
  network_id = pantechdynamics_network.test.id
  name       = "%[1]s"
  cidr       = "10.79.1.0/24"
}

resource "pantechdynamics_firewall_rule" "ssh" {
  subnet_id  = pantechdynamics_subnet.test.id
  number     = 100
  protocol   = "tcp"
  port_start = 22
  cidr       = "0.0.0.0/0"
}

resource "pantechdynamics_firewall_rule" "ping" {
  subnet_id = pantechdynamics_subnet.test.id
  number    = 110
  protocol  = "icmp"
  cidr      = "0.0.0.0/0"
}

resource "pantechdynamics_instance" "test" {
  name       = "%[1]s"
  plan_slug  = "%[2]s"
  image_slug = "%[3]s"
  ssh_key_id = pantechdynamics_ssh_key.test.id
  subnet_id  = pantechdynamics_subnet.test.id
}
`

const staticNAT = `
resource "pantechdynamics_public_ip" "test" {
  network_id  = pantechdynamics_network.test.id
  instance_id = pantechdynamics_instance.test.id
}
`

const portForward = `
resource "pantechdynamics_public_ip" "fwd" {
  network_id = pantechdynamics_network.test.id
  purpose    = "port_forwarding"
}

resource "pantechdynamics_port_forwarding_rule" "ssh" {
  public_ip_id       = pantechdynamics_public_ip.fwd.id
  instance_id        = pantechdynamics_instance.test.id
  public_port_start  = 2222
  private_port_start = 22
}
`

// seenIDs records every id of the given kinds that Terraform's state held.
type seenIDs struct{ byKind map[string][]string }

func (s *seenIDs) capture(addrs ...string) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		if s.byKind == nil {
			s.byKind = map[string][]string{}
		}
		for _, a := range addrs {
			rs, ok := st.RootModule().Resources[a]
			if !ok {
				return fmt.Errorf("%s not in state", a)
			}
			if !slices.Contains(s.byKind[a], rs.Primary.ID) {
				s.byKind[a] = append(s.byKind[a], rs.Primary.ID)
			}
		}
		return nil
	}
}

// checkAllGone fails if any instance, public IP or network the test created is
// still live. Order matters for the message only: they were already destroyed.
func (s *seenIDs) checkAllGone(t *testing.T) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		c, ctx := apiClient(t), context.Background()
		for _, id := range s.byKind["pantechdynamics_instance.test"] {
			i, err := c.GetInstance(ctx, id)
			if err != nil && !errors.Is(err, client.ErrNotFound) {
				return err
			}
			if err == nil && i.ObservedState != "deleted" {
				return fmt.Errorf("instance %s is still %s", id, i.ObservedState)
			}
		}
		for _, a := range []string{"pantechdynamics_public_ip.test", "pantechdynamics_public_ip.fwd"} {
			for _, id := range s.byKind[a] {
				ip, err := c.GetPublicIP(ctx, id)
				if err != nil && !errors.Is(err, client.ErrNotFound) {
					return err
				}
				if err == nil && ip.ObservedState != "deleted" {
					return fmt.Errorf("public ip %s is still %s", id, ip.ObservedState)
				}
			}
		}
		for _, id := range s.byKind["pantechdynamics_network.test"] {
			n, err := c.GetNetwork(ctx, id)
			if err != nil && !errors.Is(err, client.ErrNotFound) {
				return err
			}
			if err == nil && n.ObservedState != "deleted" {
				return fmt.Errorf("network %s is still %s", id, n.ObservedState)
			}
		}
		return nil
	}
}

func TestAccVPC_chain(t *testing.T) {
	requireVPCChain(t)
	c := apiClient(t)
	name := acctest.RandomWithPrefix("tfacc-vpc")
	plan := cheapestVPCPlan(t, c)
	image := os.Getenv("TF_ACC_IMAGE")
	if image == "" {
		image = "ubuntu-24-04"
	}
	base := fmt.Sprintf(chainBase, name, plan, image)
	var seen seenIDs

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             seen.checkAllGone(t),
		Steps: []resource.TestStep{
			{
				// Zero spend: a security group on a VPC instance is refused at plan time.
				Config: base + `
resource "pantechdynamics_instance" "bad" {
  name              = "bad"
  plan_slug         = "x"
  image_slug        = "x"
  subnet_id         = pantechdynamics_subnet.test.id
  security_group_id = "sg_1"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`not allowed in a VPC`),
			},
			{
				Config: base + staticNAT,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pantechdynamics_network.test", "observed_state", "active"),
					resource.TestCheckResourceAttr("pantechdynamics_subnet.test", "observed_state", "active"),
					resource.TestCheckResourceAttr("pantechdynamics_firewall_rule.ssh", "port_end", "22"),
					resource.TestCheckResourceAttr("pantechdynamics_firewall_rule.ssh", "action", "allow"),
					resource.TestCheckResourceAttrPair("pantechdynamics_instance.test", "subnet_id", "pantechdynamics_subnet.test", "id"),
					resource.TestCheckResourceAttrPair("pantechdynamics_instance.test", "network_id", "pantechdynamics_network.test", "id"),
					resource.TestCheckResourceAttrSet("pantechdynamics_public_ip.test", "address"),
					resource.TestCheckResourceAttrPair("pantechdynamics_public_ip.test", "instance_id", "pantechdynamics_instance.test", "id"),
					seen.capture("pantechdynamics_network.test", "pantechdynamics_instance.test", "pantechdynamics_public_ip.test"),
				),
			},
			{
				// No spurious diff on any resource of the chain.
				Config:   base + staticNAT,
				PlanOnly: true,
			},
			{ResourceName: "pantechdynamics_network.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "pantechdynamics_subnet.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "pantechdynamics_public_ip.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				ResourceName:            "pantechdynamics_firewall_rule.ssh",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["pantechdynamics_firewall_rule.ssh"]
					return rs.Primary.Attributes["subnet_id"] + "/" + rs.Primary.ID, nil
				},
			},
			{
				// Swap the static NAT address for a port forward: the old address is
				// released before the rule's address is allocated.
				Config: base + portForward,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pantechdynamics_public_ip.fwd", "purpose", "port_forwarding"),
					resource.TestCheckResourceAttr("pantechdynamics_port_forwarding_rule.ssh", "public_port_end", "2222"),
					resource.TestCheckResourceAttr("pantechdynamics_port_forwarding_rule.ssh", "private_port_end", "22"),
					seen.capture("pantechdynamics_public_ip.fwd"),
				),
			},
			{
				ResourceName:            "pantechdynamics_port_forwarding_rule.ssh",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["pantechdynamics_port_forwarding_rule.ssh"]
					return rs.Primary.Attributes["public_ip_id"] + "/" + rs.Primary.ID, nil
				},
			},
		},
	})
}
