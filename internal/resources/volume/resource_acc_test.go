package volume_test

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
	addr       = "pantechdynamics_volume.data"
	volPrefix  = "tfacc-vol"
	vmPrefix   = "tfacc-vol-vm"
	vmResource = "pantechdynamics_instance.vm"
)

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY.
//
// THESE TESTS SPEND REAL CREDIT. The standalone and validation tests order only
// small volumes, billed by the hour. The attachment test also orders ONE instance
// on the cheapest plan (about NGN 15,040 reserved upfront). Everything is named
// tfacc-vol-*, and a cleanup sweep removes anything a failed test leaves behind.
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// requireAcc skips the test unless TF_ACC is set. It must be the first line of
// every acceptance test: the helpers call the live API before resource.Test would
// skip, and an ordinary `go test ./...` must never reach the API or spend.
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

// newPublicKey returns a fresh, valid OpenSSH ed25519 public key.
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
func sshKey(t *testing.T) string {
	t.Helper()
	c := apiClient(t)
	key, err := c.CreateSSHKey(context.Background(), client.CreateSSHKeyRequest{Name: acctest.RandomWithPrefix("tfacc-vol-key"), PublicKey: newPublicKey(t)})
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

func volumeConfig(name, offering, extra string) string {
	return fmt.Sprintf(`
resource "pantechdynamics_volume" "data" {
  name               = %q
  disk_offering_slug = %q
  %s

  timeouts = {
    create = "20m"
    update = "30m"
    delete = "20m"
  }
}
`, name, offering, extra)
}

// forceDeleteVolume removes a volume however it was left: an attached volume is
// detached first, and a volume holding a stale attach request is cleared, because
// the platform refuses to delete either.
func forceDeleteVolume(t *testing.T, c *client.Client, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	vol, err := c.GetVolume(ctx, id)
	if err != nil || vol.ObservedState == client.VolumeDeleted {
		return
	}
	if vol.AttachedInstanceID != nil || vol.DesiredInstanceID != nil {
		if ref, err := c.DetachVolume(ctx, id); err == nil {
			_ = c.WaitForOperation(ctx, ref.OperationID, nil) // may fail on a stale request, which is fine
		}
	}
	ref, err := c.DeleteVolume(ctx, id)
	if err != nil {
		t.Logf("sweep: deleting volume %s: %v", id, err)
		return
	}
	_ = c.WaitForOperation(ctx, ref.OperationID, func(ctx context.Context) (bool, error) {
		v, err := c.GetVolume(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		return err == nil && v.ObservedState == client.VolumeDeleted, err
	})
}

// sweep removes every live tfacc-vol* volume and tfacc-vol-vm* instance. It is the
// safety net against a failed test leaving billed resources behind.
func sweep(t *testing.T) {
	t.Helper()
	c := apiClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if vols, err := c.ListVolumes(ctx); err == nil {
		for _, v := range vols {
			if strings.HasPrefix(v.Name, volPrefix) {
				t.Logf("sweep: removing leftover volume %s (%s)", v.Name, v.ID)
				forceDeleteVolume(t, c, v.ID)
			}
		}
	}
	if instances, err := c.ListInstances(ctx); err == nil {
		for _, inst := range instances {
			if strings.HasPrefix(inst.Name, vmPrefix) && inst.ObservedState != client.InstanceDeleted {
				t.Logf("sweep: deleting leftover instance %s (%s)", inst.Name, inst.ID)
				if ref, err := c.DeleteInstance(ctx, inst.ID); err == nil {
					_ = c.WaitForOperation(ctx, ref.OperationID, func(ctx context.Context) (bool, error) {
						i, err := c.GetInstance(ctx, inst.ID)
						if errors.Is(err, client.ErrNotFound) {
							return true, nil
						}
						return err == nil && i.ObservedState == client.InstanceDeleted, err
					})
				}
			}
		}
	}
}

// checkNoneLeft fails if a live tfacc-vol volume or tfacc-vol-vm instance remains.
func checkNoneLeft(t *testing.T) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		c := apiClient(t)
		vols, err := c.ListVolumes(context.Background())
		if err != nil {
			return err
		}
		for _, v := range vols {
			if strings.HasPrefix(v.Name, volPrefix) {
				return fmt.Errorf("volume %s (%s) is still %s after destroy", v.Name, v.ID, v.ObservedState)
			}
		}
		instances, err := c.ListInstances(context.Background())
		if err != nil {
			return err
		}
		for _, inst := range instances {
			if strings.HasPrefix(inst.Name, vmPrefix) && inst.ObservedState != client.InstanceDeleted {
				return fmt.Errorf("instance %s (%s) is still %s after destroy", inst.Name, inst.ID, inst.ObservedState)
			}
		}
		return nil
	}
}

func captureID(resourceAddr string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceAddr]
		if !ok {
			return fmt.Errorf("%s not in state", resourceAddr)
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

// TestAccVolume_standalone orders only small shared volumes: create, a refused
// duplicate name, import, an in-place grow, a refused shrink, a rename that
// replaces the volume, and drift. No instance is ordered.
func TestAccVolume_standalone(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix(volPrefix)
	renamed := name + "-renamed"
	t.Cleanup(func() { sweep(t) })
	var id, newID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{
				Config: volumeConfig(name, "small-5gb", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`^vol_`)),
					resource.TestCheckResourceAttr(addr, "size_gb", "5"),
					resource.TestCheckResourceAttr(addr, "storage_type", "shared"),
					resource.TestCheckResourceAttr(addr, "observed_state", "active"),
					resource.TestCheckNoResourceAttr(addr, "instance_id"),
					resource.TestCheckResourceAttrSet(addr, "zone"),
					resource.TestCheckResourceAttrSet(addr, "monthly_cost_minor"),
					captureID(addr, &id),
				),
			},
			{
				// A second volume with the same name is refused before any order.
				Config: volumeConfig(name, "small-5gb", "") + fmt.Sprintf(`
resource "pantechdynamics_volume" "dup" {
  name               = %q
  disk_offering_slug = "small-5gb"
  depends_on         = [pantechdynamics_volume.data]
}
`, name),
				ExpectError: regexp.MustCompile(`already exists`),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				Config:   volumeConfig(name, "small-5gb", ""),
				PlanOnly: true,
			},
			{
				// Grow in place: same id, bigger size.
				Config: volumeConfig(name, "shared-10gb", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "disk_offering_slug", "shared-10gb"),
					resource.TestCheckResourceAttr(addr, "size_gb", "10"),
					checkID(&id, true),
				),
			},
			{
				Config:   volumeConfig(name, "shared-10gb", ""),
				PlanOnly: true,
			},
			{
				// A volume never shrinks, and the provider says so before sending anything.
				Config:      volumeConfig(name, "small-5gb", ""),
				ExpectError: regexp.MustCompile(`Volumes can only grow`),
			},
			{
				// A rename replaces the volume (there is no rename endpoint).
				Config: volumeConfig(renamed, "shared-10gb", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", renamed),
					captureID(addr, &newID),
					checkID(&id, false),
				),
			},
			{
				// Delete behind Terraform's back, then refresh: Read drops it from state.
				PreConfig: func() {
					forceDeleteVolume(t, apiClient(t), newID)
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccVolume_validationIsFree checks plan-time and pre-order validation. Every
// step fails before a volume is ordered, or is a plan only, so nothing is spent.
func TestAccVolume_validationIsFree(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix(volPrefix)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{Config: volumeConfig(name, "small-5gb", `mount_point = "data"`), ExpectError: regexp.MustCompile(`Invalid value`)},
			{Config: volumeConfig(name, "small-5gb", `instance_id = "vol_wrong_kind"`), ExpectError: regexp.MustCompile(`Invalid value`)},
			{Config: volumeConfig("", "small-5gb", ""), ExpectError: regexp.MustCompile(`Invalid value`)},
			{
				// The API would silently ignore size_gb and make 5 GB, so the provider refuses.
				Config:      volumeConfig(name, "small-5gb", `size_gb = 50`),
				ExpectError: regexp.MustCompile(`Invalid disk offering or size`),
			},
			{
				Config:      volumeConfig(name, "custom", ""),
				ExpectError: regexp.MustCompile(`Invalid disk offering or size`),
			},
			{
				Config:      volumeConfig(name, "no-such-offering", ""),
				ExpectError: regexp.MustCompile(`Invalid disk offering or size`),
			},
			{
				// A valid config, planned only: shows the create and orders nothing.
				Config:             volumeConfig(name, "small-5gb", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccVolume_importRejectsWrongID(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config:        volumeConfig("tfacc-vol-import-check", "small-5gb", ""),
				ResourceName:  addr,
				ImportState:   true,
				ImportStateId: "vm_wrong_prefix",
				ExpectError:   regexp.MustCompile(`Expected an id starting with "vol_"`),
			},
		},
	})
}

// TestAccVolume_attachment orders ONE instance (about NGN 15,040) and a local
// volume. It covers attach, a plan-time refusal to resize while attached, an
// in-place detach, re-attach, and a shared volume whose attach fails: the final
// destroy must still remove it, which needs the stale attach request cleared.
func TestAccVolume_attachment(t *testing.T) {
	requireAcc(t)
	keyID := sshKey(t)
	vmName := acctest.RandomWithPrefix(vmPrefix)
	local := acctest.RandomWithPrefix(volPrefix + "-local")
	shared := acctest.RandomWithPrefix(volPrefix + "-shared")
	t.Cleanup(func() { sweep(t) })
	var id string

	instance := fmt.Sprintf(`
resource "pantechdynamics_instance" "vm" {
  name       = %q
  plan_slug  = "individual"
  image_slug = "ubuntu-24-04"
  ssh_key_id = %q

  timeouts = {
    create = "20m"
    delete = "20m"
  }
}
`, vmName, keyID)
	localVolume := func(offering, instanceID string) string {
		return instance + volumeConfig(local, offering, instanceID)
	}
	attached := `instance_id = pantechdynamics_instance.vm.id`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{
				// Created standalone, then attached in its own step.
				Config: localVolume("small-local-20gb", attached),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_type", "local"),
					resource.TestCheckResourceAttr(addr, "size_gb", "20"),
					resource.TestCheckResourceAttrPair(addr, "instance_id", vmResource, "id"),
					captureID(addr, &id),
				),
			},
			{
				Config:   localVolume("small-local-20gb", attached),
				PlanOnly: true,
			},
			{
				// The platform only resizes a detached volume, so a resize while attached
				// is refused at plan time. Nothing is sent. (Staging has a single local
				// offering, so a bigger shared one stands in for the target.)
				Config:      localVolume("large-100gb", attached),
				ExpectError: regexp.MustCompile(`Detach the volume before resizing`),
			},
			{
				// Detach in place: remove instance_id, the volume keeps its id and data.
				Config: localVolume("small-local-20gb", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "instance_id"),
					resource.TestCheckResourceAttr(addr, "size_gb", "20"),
					checkID(&id, true),
				),
			},
			{
				// Attach it again.
				Config: localVolume("small-local-20gb", attached),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(addr, "instance_id", vmResource, "id"),
					checkID(&id, true),
				),
			},
			{
				// A shared volume cannot attach on this platform. The error says why, and
				// the volume stays deletable: the final destroy removes it.
				Config: localVolume("small-local-20gb", attached) + fmt.Sprintf(`
resource "pantechdynamics_volume" "shared" {
  name               = %q
  disk_offering_slug = "small-5gb"
  instance_id        = pantechdynamics_instance.vm.id

  timeouts = {
    create = "20m"
    delete = "20m"
  }
}
`, shared),
				ExpectError: regexp.MustCompile(`Error attaching the volume`),
			},
		},
	})
}
