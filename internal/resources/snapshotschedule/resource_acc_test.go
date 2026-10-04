package snapshotschedule_test

import (
	"context"
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
	prefix       = "tfacc-sched"
	schedAddr    = "pantechdynamics_snapshot_schedule.test"
	volumeAddr   = "pantechdynamics_volume.data"
	volumeConfig = `
resource "pantechdynamics_volume" "data" {
  name               = %q
  disk_offering_slug = "small-5gb"

  timeouts = {
    create = "20m"
    delete = "20m"
  }
}
`
)

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY.
//
// THESE TESTS SPEND A LITTLE REAL CREDIT: they order one small shared volume, billed
// by the hour, and no instance. A schedule can queue an automatic snapshot, so the
// cleanup sweep deletes any snapshot of the test volume as well as the volume.
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

func scheduleConfig(volName, body string) string {
	return fmt.Sprintf(volumeConfig, volName) + fmt.Sprintf(`
resource "pantechdynamics_snapshot_schedule" "test" {
  volume_id = pantechdynamics_volume.data.id
%s}
`, body)
}

// sweep removes every snapshot of a tfacc-sched volume, then the volumes.
func sweep(t *testing.T) {
	t.Helper()
	c := apiClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	owned := map[string]bool{}
	vols, err := c.ListVolumes(ctx)
	if err != nil {
		return
	}
	for _, v := range vols {
		if strings.HasPrefix(v.Name, prefix) {
			owned[v.ID] = true
		}
	}
	if snaps, err := c.ListSnapshots(ctx); err == nil {
		for _, s := range snaps {
			if s.VolumeID != nil && owned[*s.VolumeID] {
				t.Logf("sweep: deleting snapshot %s (%s)", s.Name, s.ID)
				if ref, err := c.DeleteSnapshot(ctx, s.ID); err == nil {
					id := s.ID
					_ = c.WaitForOperation(ctx, ref.OperationID, func(ctx context.Context) (bool, error) {
						got, err := c.GetSnapshot(ctx, id)
						return errors.Is(err, client.ErrNotFound) || (err == nil && got.ObservedState == client.SnapshotDeleted), nil
					})
				}
			}
		}
	}
	for id := range owned {
		t.Logf("sweep: deleting volume %s", id)
		if ref, err := c.DeleteVolume(ctx, id); err == nil {
			vid := id
			_ = c.WaitForOperation(ctx, ref.OperationID, func(ctx context.Context) (bool, error) {
				got, err := c.GetVolume(ctx, vid)
				return errors.Is(err, client.ErrNotFound) || (err == nil && got.ObservedState == client.VolumeDeleted), nil
			})
		}
	}
}

// checkNoneLeft fails if a live tfacc-sched volume remains.
func checkNoneLeft(t *testing.T) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		vols, err := apiClient(t).ListVolumes(context.Background())
		if err != nil {
			return err
		}
		for _, v := range vols {
			if strings.HasPrefix(v.Name, prefix) {
				return fmt.Errorf("volume %s (%s) is still %s after destroy", v.Name, v.ID, v.ObservedState)
			}
		}
		return nil
	}
}

// checkPaused asks the API directly: after the schedule resource is destroyed, the
// volume's schedule must still exist and be paused, because the API cannot delete one.
func checkPaused(t *testing.T) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[volumeAddr]
		if !ok {
			return fmt.Errorf("%s not in state", volumeAddr)
		}
		sched, err := apiClient(t).GetVolumeSnapshotSchedule(context.Background(), rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("the schedule should still exist, paused: %w", err)
		}
		if sched.Enabled {
			return fmt.Errorf("the schedule is still enabled after destroy: %+v", sched)
		}
		return nil
	}
}

// TestAccSnapshotSchedule_lifecycle covers create, an in-place change, pausing,
// import, and the destroy that pauses the schedule instead of deleting it. It orders
// one small shared volume and no instance.
func TestAccSnapshotSchedule_lifecycle(t *testing.T) {
	requireAcc(t)
	volName := acctest.RandomWithPrefix(prefix)
	t.Cleanup(func() { sweep(t) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{
				// Created paused, so no automatic snapshot is queued. The defaults for
				// retention_count (7) and enabled (true) are overridden for enabled only.
				Config: scheduleConfig(volName, "  frequency = \"daily\"\n  enabled   = false\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(schedAddr, "id", volumeAddr, "id"),
					resource.TestCheckResourceAttrPair(schedAddr, "volume_id", volumeAddr, "id"),
					resource.TestCheckResourceAttr(schedAddr, "frequency", "daily"),
					resource.TestCheckResourceAttr(schedAddr, "retention_count", "7"),
					resource.TestCheckResourceAttr(schedAddr, "enabled", "false"),
					resource.TestCheckNoResourceAttr(schedAddr, "instance_id"),
					resource.TestCheckResourceAttrSet(schedAddr, "next_run_at"),
				),
			},
			{
				Config:   scheduleConfig(volName, "  frequency = \"daily\"\n  enabled   = false\n"),
				PlanOnly: true,
			},
			{
				// Change it in place.
				Config: scheduleConfig(volName, "  frequency       = \"weekly\"\n  retention_count = 3\n  enabled         = false\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schedAddr, "frequency", "weekly"),
					resource.TestCheckResourceAttr(schedAddr, "retention_count", "3"),
				),
			},
			{
				ResourceName:      schedAddr,
				ImportState:       true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) { return s.RootModule().Resources[volumeAddr].Primary.ID, nil },
				ImportStateVerify: true,
				// next_run_at moves as the platform runs the schedule.
				ImportStateVerifyIgnore: []string{"next_run_at"},
			},
			{
				// Enable it: the platform queues the first copy on its next tick.
				Config: scheduleConfig(volName, "  frequency       = \"weekly\"\n  retention_count = 3\n"),
				Check:  resource.TestCheckResourceAttr(schedAddr, "enabled", "true"),
			},
			{
				// Remove the schedule resource only: the API cannot delete a schedule, so
				// the provider pauses it, and the volume's schedule is still there, paused.
				Config: fmt.Sprintf(volumeConfig, volName),
				Check:  checkPaused(t),
			},
		},
	})
}

// TestAccSnapshotSchedule_validationIsFree checks plan-time validation. Every step
// fails before anything is ordered, so nothing is spent.
func TestAccSnapshotSchedule_validationIsFree(t *testing.T) {
	requireAcc(t)
	cfg := func(body string) string {
		return "resource \"pantechdynamics_snapshot_schedule\" \"test\" {\n" + body + "}\n"
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkNoneLeft(t),
		Steps: []resource.TestStep{
			{Config: cfg("  frequency = \"daily\"\n"), ExpectError: regexp.MustCompile(`Exactly one source is required`)},
			{Config: cfg("  instance_id = \"vm_1\"\n  volume_id = \"vol_1\"\n  frequency = \"daily\"\n"), ExpectError: regexp.MustCompile(`Exactly one source is required`)},
			{Config: cfg("  volume_id = \"vol_1\"\n  frequency = \"hourly\"\n"), ExpectError: regexp.MustCompile(`Invalid value`)},
			{Config: cfg("  volume_id = \"vol_1\"\n  frequency = \"daily\"\n  retention_count = 0\n"), ExpectError: regexp.MustCompile(`Invalid retention count`)},
			{Config: cfg("  volume_id = \"vol_1\"\n  frequency = \"daily\"\n  retention_count = 169\n"), ExpectError: regexp.MustCompile(`Invalid retention count`)},
			{Config: cfg("  volume_id = \"vm_wrong_kind\"\n  frequency = \"daily\"\n"), ExpectError: regexp.MustCompile(`Invalid value`)},
			{
				Config:             cfg("  volume_id = \"vol_1\"\n  frequency = \"daily\"\n"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccSnapshotSchedule_importRejectsWrongID(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config:        "resource \"pantechdynamics_snapshot_schedule\" \"test\" {\n  volume_id = \"vol_1\"\n  frequency = \"daily\"\n}\n",
				ResourceName:  schedAddr,
				ImportState:   true,
				ImportStateId: "snap_wrong_kind",
				ExpectError:   regexp.MustCompile(`Import a schedule by the id`),
			},
		},
	})
}
