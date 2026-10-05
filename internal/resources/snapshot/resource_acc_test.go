package snapshot_test

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
	prefix     = "tfacc-snap"
	volAddr    = "pantechdynamics_snapshot.vol"
	instAddr   = "pantechdynamics_snapshot.inst"
	vmResource = "pantechdynamics_instance.vm"
	dataVolume = "pantechdynamics_volume.data"
)

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY.
//
// THESE TESTS SPEND REAL CREDIT. The lifecycle test orders ONE instance on the
// cheapest plan (about NGN 15,040 reserved upfront), one small local volume, and a
// few snapshots, which are billed for storage. The validation test orders nothing.
// Everything is named tfacc-snap*, and a cleanup sweep removes anything a failed
// test leaves behind, including snapshots a schedule made on its own.
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

// sshKey registers a throwaway key through the API and removes it afterwards.
func sshKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var wire []byte
	for _, field := range [][]byte{[]byte("ssh-ed25519"), pub} {
		wire = binary.BigEndian.AppendUint32(wire, uint32(len(field)))
		wire = append(wire, field...)
	}
	c := apiClient(t)
	key, err := c.CreateSSHKey(context.Background(), client.CreateSSHKeyRequest{
		Name: acctest.RandomWithPrefix(prefix + "-key"), PublicKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire) + " tfacc",
	})
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

// waitDeleted waits for a delete operation and for the resource to read as gone.
func waitDeleted(c *client.Client, ref *client.OperationReference, gone client.DoneCheck) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	_ = c.WaitForOperation(ctx, ref.OperationID, gone)
}

// sweep removes everything the test may have left: every live tfacc-snap snapshot,
// every tfacc-snap volume (detached first, which also clears a stale attach request)
// and every tfacc-snap instance. The snapshots go first, because a volume or
// instance is better deleted without them.
func sweep(t *testing.T) {
	t.Helper()
	c := apiClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	owned := map[string]bool{} // ids of tfacc volumes and instances, to find snapshots made by a schedule
	if vols, err := c.ListVolumes(ctx); err == nil {
		for _, v := range vols {
			if strings.HasPrefix(v.Name, prefix) {
				owned[v.ID] = true
			}
		}
	}
	if instances, err := c.ListInstances(ctx); err == nil {
		for _, inst := range instances {
			if strings.HasPrefix(inst.Name, prefix) && inst.ObservedState != client.InstanceDeleted {
				owned[inst.ID] = true
			}
		}
	}

	if snaps, err := c.ListSnapshots(ctx); err == nil {
		for _, s := range snaps {
			mine := strings.HasPrefix(s.Name, prefix) || (s.VolumeID != nil && owned[*s.VolumeID]) || (s.InstanceID != nil && owned[*s.InstanceID])
			if !mine {
				continue
			}
			t.Logf("sweep: deleting snapshot %s (%s)", s.Name, s.ID)
			if ref, err := c.DeleteSnapshot(ctx, s.ID); err == nil {
				id := s.ID
				waitDeleted(c, ref, func(ctx context.Context) (bool, error) {
					got, err := c.GetSnapshot(ctx, id)
					return errors.Is(err, client.ErrNotFound) || (err == nil && got.ObservedState == client.SnapshotDeleted), nil
				})
			}
		}
	}
	if vols, err := c.ListVolumes(ctx); err == nil {
		for _, v := range vols {
			if !strings.HasPrefix(v.Name, prefix) {
				continue
			}
			t.Logf("sweep: removing volume %s (%s)", v.Name, v.ID)
			if v.AttachedInstanceID != nil || v.DesiredInstanceID != nil {
				if ref, err := c.DetachVolume(ctx, v.ID); err == nil {
					_ = c.WaitForOperation(ctx, ref.OperationID, nil) // may fail on a stale request, which is fine
				}
			}
			if ref, err := c.DeleteVolume(ctx, v.ID); err == nil {
				id := v.ID
				waitDeleted(c, ref, func(ctx context.Context) (bool, error) {
					got, err := c.GetVolume(ctx, id)
					return errors.Is(err, client.ErrNotFound) || (err == nil && got.ObservedState == client.VolumeDeleted), nil
				})
			}
		}
	}
	if instances, err := c.ListInstances(ctx); err == nil {
		for _, inst := range instances {
			if strings.HasPrefix(inst.Name, prefix) && inst.ObservedState != client.InstanceDeleted {
				t.Logf("sweep: deleting instance %s (%s)", inst.Name, inst.ID)
				if ref, err := c.DeleteInstance(ctx, inst.ID); err == nil {
					id := inst.ID
					waitDeleted(c, ref, func(ctx context.Context) (bool, error) {
						got, err := c.GetInstance(ctx, id)
						return errors.Is(err, client.ErrNotFound) || (err == nil && got.ObservedState == client.InstanceDeleted), nil
					})
				}
			}
		}
	}
}

// checkNoneLeft fails if a live tfacc-snap snapshot, volume or instance remains.
func checkNoneLeft(t *testing.T) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		c := apiClient(t)
		ctx := context.Background()
		snaps, err := c.ListSnapshots(ctx)
		if err != nil {
			return err
		}
		for _, s := range snaps {
			if strings.HasPrefix(s.Name, prefix) {
				return fmt.Errorf("snapshot %s (%s) is still %s after destroy", s.Name, s.ID, s.ObservedState)
			}
		}
		vols, err := c.ListVolumes(ctx)
		if err != nil {
			return err
		}
		for _, v := range vols {
			if strings.HasPrefix(v.Name, prefix) {
				return fmt.Errorf("volume %s (%s) is still %s after destroy", v.Name, v.ID, v.ObservedState)
			}
		}
		instances, err := c.ListInstances(ctx)
		if err != nil {
			return err
		}
		for _, inst := range instances {
			if strings.HasPrefix(inst.Name, prefix) && inst.ObservedState != client.InstanceDeleted {
				return fmt.Errorf("instance %s (%s) is still %s after destroy", inst.Name, inst.ID, inst.ObservedState)
			}
		}
		return nil
	}
}

func captureID(addr string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		*dst = rs.Primary.ID
		return nil
	}
}

// instanceAndVolume is an instance with a local volume attached to it, the setup in
// which a volume snapshot worked on staging.
func instanceAndVolume(vmName, keyID, desired, volName string) string {
	return fmt.Sprintf(`
resource "pantechdynamics_instance" "vm" {
  name          = %q
  plan_slug     = "individual"
  image_slug    = "ubuntu-24-04"
  ssh_key_id    = %q
  desired_state = %q

  timeouts = {
    create = "20m"
    update = "20m"
    delete = "20m"
  }
}

resource "pantechdynamics_volume" "data" {
  name               = %q
  disk_offering_slug = "small-local-20gb"
  instance_id        = pantechdynamics_instance.vm.id

  timeouts = {
    create = "20m"
    update = "30m"
    delete = "20m"
  }
}
`, vmName, keyID, desired, volName)
}

func volumeSnapshot(name string) string {
	return fmt.Sprintf(`
resource "pantechdynamics_snapshot" "vol" {
  name      = %q
  volume_id = pantechdynamics_volume.data.id

  timeouts = {
    create = "20m"
    delete = "20m"
  }
}
`, name)
}

// TestAccSnapshot_lifecycle orders ONE instance and a local volume attached to it,
// and snapshots the volume. It covers a refused duplicate name, import, a snapshot
// removed (its name then stays reserved, so making it again is refused), a new name,
// and a clean destroy.
func TestAccSnapshot_lifecycle(t *testing.T) {
	requireAcc(t)
	keyID := sshKey(t)
	vmName := acctest.RandomWithPrefix(prefix + "-vm")
	volName := acctest.RandomWithPrefix(prefix + "-vol")
	snapName := acctest.RandomWithPrefix(prefix + "-v")
	renamed := snapName + "-renamed"
	t.Cleanup(func() { sweep(t) })
	var firstID string
	infra := instanceAndVolume(vmName, keyID, "running", volName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{
				Config: infra + volumeSnapshot(snapName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(volAddr, "id", regexp.MustCompile(`^snap_`)),
					resource.TestCheckResourceAttr(volAddr, "name", snapName),
					resource.TestCheckResourceAttr(volAddr, "observed_state", "active"),
					resource.TestCheckResourceAttr(volAddr, "trigger", "manual"),
					resource.TestCheckResourceAttrPair(volAddr, "volume_id", dataVolume, "id"),
					resource.TestCheckNoResourceAttr(volAddr, "instance_id"),
					resource.TestCheckResourceAttrSet(volAddr, "completed_at"),
					captureID(volAddr, &firstID),
				),
			},
			{
				// A second snapshot of the same volume with the same name is refused.
				Config: infra + volumeSnapshot(snapName) + fmt.Sprintf(`
resource "pantechdynamics_snapshot" "dup" {
  name       = %q
  volume_id  = pantechdynamics_volume.data.id
  depends_on = [pantechdynamics_snapshot.vol]
}
`, snapName),
				ExpectError: regexp.MustCompile(`Snapshot name already in use`),
			},
			{
				ResourceName:            volAddr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				Config:   infra + volumeSnapshot(snapName),
				PlanOnly: true,
			},
			{
				// Remove the snapshot...
				Config: infra,
			},
			{
				// ...and make it again under the same name: the platform keeps a deleted
				// snapshot's name reserved, so this is refused, and the provider says why.
				Config:      infra + volumeSnapshot(snapName),
				ExpectError: regexp.MustCompile(`Snapshot name already in use`),
			},
			{
				// A new name works.
				Config: infra + volumeSnapshot(renamed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(volAddr, "name", renamed),
					resource.TestCheckResourceAttr(volAddr, "observed_state", "active"),
				),
			},
		},
	})
}

// TestAccSnapshot_instanceRootDisk snapshots a stopped instance's root disk. It is
// OFF by default: on staging that worked once and failed twice, including on a stopped
// instance, so a default test would fail for reasons that are not the provider's.
// Run it with TF_ACC_SNAPSHOT_INSTANCE=1. It orders ONE instance and no volume.
func TestAccSnapshot_instanceRootDisk(t *testing.T) {
	requireAcc(t)
	if os.Getenv("TF_ACC_SNAPSHOT_INSTANCE") == "" {
		t.Skip("set TF_ACC_SNAPSHOT_INSTANCE=1 to run: root-disk snapshots are unreliable on staging")
	}
	keyID := sshKey(t)
	vmName := acctest.RandomWithPrefix(prefix + "-vm")
	snapName := acctest.RandomWithPrefix(prefix + "-i")
	t.Cleanup(func() { sweep(t) })

	config := fmt.Sprintf(`
resource "pantechdynamics_instance" "vm" {
  name          = %q
  plan_slug     = "individual"
  image_slug    = "ubuntu-24-04"
  ssh_key_id    = %q
  desired_state = "stopped"

  timeouts = {
    create = "20m"
    update = "20m"
    delete = "20m"
  }
}

resource "pantechdynamics_snapshot" "inst" {
  name        = %q
  instance_id = pantechdynamics_instance.vm.id

  timeouts = {
    create = "30m"
    delete = "20m"
  }
}
`, vmName, keyID, snapName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(instAddr, "observed_state", "active"),
					resource.TestCheckResourceAttrPair(instAddr, "instance_id", vmResource, "id"),
					resource.TestCheckNoResourceAttr(instAddr, "volume_id"),
				),
			},
		},
	})
}

// TestAccSnapshot_validationIsFree checks plan-time validation. Every step fails
// before anything is ordered, or is a plan only, so nothing is spent.
func TestAccSnapshot_validationIsFree(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix(prefix)
	cfg := func(body string) string {
		return fmt.Sprintf("resource \"pantechdynamics_snapshot\" \"vol\" {\n  name = %q\n%s}\n", name, body)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{Config: cfg(""), ExpectError: regexp.MustCompile(`Exactly one source is required`)},
			{Config: cfg("  instance_id = \"vm_1\"\n  volume_id   = \"vol_1\"\n"), ExpectError: regexp.MustCompile(`Exactly one source is required`)},
			{Config: cfg("  instance_id = \"vol_wrong_kind\"\n"), ExpectError: regexp.MustCompile(`Invalid value`)},
			{Config: cfg("  volume_id = \"vm_wrong_kind\"\n"), ExpectError: regexp.MustCompile(`Invalid value`)},
			{
				Config:      "resource \"pantechdynamics_snapshot\" \"vol\" {\n  name      = \"\"\n  volume_id = \"vol_1\"\n}\n",
				ExpectError: regexp.MustCompile(`Invalid value`),
			},
			{
				// A valid config, planned only: shows the create and orders nothing.
				Config:             cfg("  volume_id = \"vol_1\"\n"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccSnapshot_importRejectsWrongID(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config:        "resource \"pantechdynamics_snapshot\" \"vol\" {\n  name      = \"tfacc-snap-import\"\n  volume_id = \"vol_1\"\n}\n",
				ResourceName:  volAddr,
				ImportState:   true,
				ImportStateId: "vol_wrong_prefix",
				ExpectError:   regexp.MustCompile(`Expected an id starting with "snap_"`),
			},
		},
	})
}
