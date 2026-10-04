package instance_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
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

const (
	addr       = "pantechdynamics_instance.test"
	namePrefix = "tfacc-vm"
)

// Acceptance tests run only with TF_ACC=1, against the dev API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY.
//
// THESE TESTS SPEND REAL CREDIT. A full run orders exactly one instance on the
// cheapest plan (about NGN 15,040 reserved upfront). The validation test and the
// duplicate-name step order nothing. Every instance is named tfacc-vm-*, and a
// cleanup sweep deletes any that a failed test leaves behind.
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// requireAcc skips the test unless TF_ACC is set. It must be the first line of
// every acceptance test: the helpers below call the live API before resource.Test
// would skip, and an ordinary `go test ./...` must never reach the API or spend.
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

// newPublicKey returns a fresh, valid OpenSSH ed25519 public key. The backend
// rejects a key that is already registered, so each test needs its own.
func newPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const keyType = "ssh-ed25519"
	var wire []byte
	for _, field := range [][]byte{[]byte(keyType), pub} {
		wire = binary.BigEndian.AppendUint32(wire, uint32(len(field)))
		wire = append(wire, field...)
	}
	return keyType + " " + base64.StdEncoding.EncodeToString(wire) + " tfacc"
}

// sshKey registers a throwaway key through the API and removes it afterwards.
// Removal is retried because dev deletes have timed out. A key left behind costs
// nothing, so a final failure is only logged.
func sshKey(t *testing.T) string {
	t.Helper()
	c := apiClient(t)
	key, err := c.CreateSSHKey(context.Background(), client.CreateSSHKeyRequest{Name: acctest.RandomWithPrefix("tfacc-vm-key"), PublicKey: newPublicKey(t)})
	if err != nil {
		t.Fatalf("creating the throwaway ssh key: %v", err)
	}
	t.Cleanup(func() {
		for attempt := 1; attempt <= 3; attempt++ {
			if err := c.DeleteSSHKey(context.Background(), key.ID); err == nil || errors.Is(err, client.ErrNotFound) {
				return
			}
		}
		t.Logf("could not delete the throwaway ssh key %s (%s); remove it by hand", key.Name, key.ID)
	})
	return key.ID
}

// config renders the instance under test on the default plan and security group.
// desired is "" to leave desired_state out, so its default (running) applies.
func config(name, keyID, desired, extra string) string {
	return configWith(name, keyID, desired, "individual", "", extra)
}

// configWith also sets the plan and, when groupRef is not empty, the security
// group, for example "pantechdynamics_security_group.alt.id".
func configWith(name, keyID, desired, planSlug, groupRef, extra string) string {
	desiredLine, groupLine := "", ""
	if desired != "" {
		desiredLine = fmt.Sprintf("desired_state = %q", desired)
	}
	if groupRef != "" {
		groupLine = "security_group_id = " + groupRef
	}
	return fmt.Sprintf(`
resource "pantechdynamics_instance" "test" {
  name       = %q
  plan_slug  = %q
  image_slug = "ubuntu-24-04"
  ssh_key_id = %q
  tags       = { purpose = "tfacc" }
  %s
  %s

  timeouts = {
    create = "20m"
    update = "20m"
    delete = "20m"
  }
}
%s
`, name, planSlug, keyID, desiredLine, groupLine, extra)
}

// altGroup is a second security group for the group change step. Its name is
// random, because the platform keeps a deleted group's name reserved.
func altGroup(name string) string {
	return fmt.Sprintf(`
resource "pantechdynamics_security_group" "alt" {
  name  = %q
  rules = [{ direction = "ingress", protocol = "icmp", cidr = "0.0.0.0/0" }]
}
`, name)
}

// sweepInstances deletes every live tfacc-vm-* instance. It is the safety net
// against a failed test leaving a paid instance running.
func sweepInstances(t *testing.T) {
	t.Helper()
	c := apiClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	instances, err := c.ListInstances(ctx)
	if err != nil {
		t.Logf("sweep: could not list instances: %v", err)
		return
	}
	for _, inst := range instances {
		if strings.HasPrefix(inst.Name, namePrefix) && inst.ObservedState != client.InstanceDeleted {
			t.Logf("sweep: deleting leftover instance %s (%s)", inst.Name, inst.ID)
			deleteAndWait(t, c, inst.ID)
		}
	}
}

// deleteAndWait deletes an instance and waits until it reads as deleted or is gone.
func deleteAndWait(t *testing.T, c *client.Client, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if _, err := c.DeleteInstance(ctx, id); err != nil && !errors.Is(err, client.ErrNotFound) {
		t.Logf("deleting %s: %v", id, err)
		return
	}
	err := c.WaitUntil(ctx, "instance "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		inst, err := c.GetInstance(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return inst.ObservedState == client.InstanceDeleted, nil
	})
	if err != nil {
		t.Logf("waiting for %s to be deleted: %v", id, err)
	}
}

// checkNoneLeft fails if a live tfacc-vm-* instance remains.
func checkNoneLeft(t *testing.T) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		instances, err := apiClient(t).ListInstances(context.Background())
		if err != nil {
			return err
		}
		for _, inst := range instances {
			if strings.HasPrefix(inst.Name, namePrefix) && inst.ObservedState != client.InstanceDeleted {
				return fmt.Errorf("instance %s (%s) is still %s after destroy", inst.Name, inst.ID, inst.ObservedState)
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

func checkSameID(want *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		if rs.Primary.ID != *want {
			return fmt.Errorf("id changed from %q to %q: expected an in-place update", *want, rs.Primary.ID)
		}
		return nil
	}
}

// TestAccInstance_lifecycle orders ONE instance (about NGN 15,040) and takes it
// through create, duplicate-name refusal, import, an in-place rename, stop and
// start, a security group change, a resize to a bigger plan, a refused downgrade,
// and drift.
func TestAccInstance_lifecycle(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix(namePrefix)
	renamed := name + "-renamed"
	groupName := acctest.RandomWithPrefix("tfacc-vm-sg")
	keyID := sshKey(t)
	t.Cleanup(func() { sweepInstances(t) })
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{
				Config: config(name, keyID, "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestCheckResourceAttr(addr, "desired_state", "running"),
					resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`^vm_`)),
					resource.TestCheckResourceAttr(addr, "observed_state", "running"),
					resource.TestCheckResourceAttr(addr, "plan_slug", "individual"),
					resource.TestCheckResourceAttr(addr, "image_slug", "ubuntu-24-04"),
					resource.TestCheckResourceAttr(addr, "region", "af-abj"),
					resource.TestCheckResourceAttrSet(addr, "zone"),
					resource.TestCheckResourceAttrSet(addr, "security_group_id"),
					resource.TestCheckResourceAttrSet(addr, "private_ipv4"),
					resource.TestCheckResourceAttr(addr, "tags.purpose", "tfacc"),
					captureID(&id),
				),
			},
			{
				// A second instance with the same name is refused before any order,
				// so this step spends nothing. depends_on makes it check after the
				// first one exists.
				Config: config(name, keyID, "", fmt.Sprintf(`
resource "pantechdynamics_instance" "dup" {
  name       = %q
  plan_slug  = "individual"
  image_slug = "ubuntu-24-04"
  depends_on = [pantechdynamics_instance.test]
}
`, name)),
				ExpectError: regexp.MustCompile(`already exists`),
			},
			{
				// Import by id. ssh_key_id is not returned by the API.
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"ssh_key_id", "timeouts"},
			},
			{
				// A rename is in place: same id.
				Config: config(renamed, keyID, "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", renamed),
					checkSameID(&id),
				),
			},
			{
				// A second plan must be empty: no spurious diff.
				Config:   config(renamed, keyID, "", ""),
				PlanOnly: true,
			},
			{
				// Stop it in place: same id, and the platform reports it stopped.
				Config: config(renamed, keyID, "stopped", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "desired_state", "stopped"),
					resource.TestCheckResourceAttr(addr, "observed_state", "stopped"),
					checkSameID(&id),
				),
			},
			{
				Config:   config(renamed, keyID, "stopped", ""),
				PlanOnly: true,
			},
			{
				// Start it again. Leaving desired_state out means running.
				Config: config(renamed, keyID, "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "desired_state", "running"),
					resource.TestCheckResourceAttr(addr, "observed_state", "running"),
					checkSameID(&id),
				),
			},
			{
				Config:   config(renamed, keyID, "", ""),
				PlanOnly: true,
			},
			{
				// Change the security group in place: the instance is stopped, switched
				// and started again, and keeps its id.
				Config: configWith(renamed, keyID, "", "individual", "pantechdynamics_security_group.alt.id", altGroup(groupName)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(addr, "security_group_id", "pantechdynamics_security_group.alt", "id"),
					resource.TestCheckResourceAttr(addr, "desired_state", "running"),
					resource.TestCheckResourceAttr(addr, "observed_state", "running"),
					checkSameID(&id),
				),
			},
			{
				Config:   configWith(renamed, keyID, "", "individual", "pantechdynamics_security_group.alt.id", altGroup(groupName)),
				PlanOnly: true,
			},
			{
				// Resize to a bigger plan in place. This takes several minutes and the
				// disk grows with the plan.
				Config: configWith(renamed, keyID, "", "starter", "pantechdynamics_security_group.alt.id", altGroup(groupName)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "plan_slug", "starter"),
					resource.TestCheckResourceAttr(addr, "observed_state", "running"),
					checkSameID(&id),
				),
			},
			{
				Config:   configWith(renamed, keyID, "", "starter", "pantechdynamics_security_group.alt.id", altGroup(groupName)),
				PlanOnly: true,
			},
			{
				// A smaller plan is refused by the platform, and the provider says why.
				// Nothing is changed or charged.
				Config:      configWith(renamed, keyID, "", "individual", "pantechdynamics_security_group.alt.id", altGroup(groupName)),
				ExpectError: regexp.MustCompile(`Instances can only be resized`),
			},
			{
				// Delete the instance behind Terraform's back, then refresh: Read must
				// drop it from state, so the plan wants to create it again. The test
				// stops here, so no second instance is ordered.
				PreConfig:          func() { deleteAndWait(t, apiClient(t), id) },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccInstance_validationIsFreeAndOrdersNothing checks plan-time validation.
// Every step either fails before any API call or is a plan only, so it spends
// nothing and creates nothing.
func TestAccInstance_validationIsFreeAndOrdersNothing(t *testing.T) {
	requireAcc(t)
	bad := func(name string) string {
		return fmt.Sprintf(`
resource "pantechdynamics_instance" "test" {
  name       = %q
  plan_slug  = "individual"
  image_slug = "ubuntu-24-04"
}
`, name)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{Config: bad("has space"), ExpectError: regexp.MustCompile(`Invalid name`)},
			{Config: bad("under_score"), ExpectError: regexp.MustCompile(`Invalid name`)},
			{Config: bad("-leading"), ExpectError: regexp.MustCompile(`Invalid name`)},
			{Config: bad(strings.Repeat("a", 64)), ExpectError: regexp.MustCompile(`Invalid name`)},
			{
				Config: `
resource "pantechdynamics_instance" "test" {
  name          = "tfacc-vm-state"
  plan_slug     = "individual"
  image_slug    = "ubuntu-24-04"
  desired_state = "deleted"
}
`,
				ExpectError: regexp.MustCompile(`Invalid value`),
			},
			{
				// A valid config, planned only: shows the create and orders nothing.
				Config:             bad(acctest.RandomWithPrefix(namePrefix)),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccInstance_importRejectsWrongID(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config:             config("tfacc-vm-import-check", "sshk_unused", "", ""),
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      "sg_wrong_prefix",
				ImportStatePersist: false,
				ExpectError:        regexp.MustCompile(`Expected an id starting with "vm_"`),
			},
		},
	})
}
