package volume

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func TestToCreateRequest(t *testing.T) {
	t.Run("everything set", func(t *testing.T) {
		req := toCreateRequest(model{Name: types.StringValue("data"), DiskOfferingSlug: types.StringValue("custom"), SizeGB: types.Int64Value(50),
			Region: types.StringValue("af-abj"), MountPoint: types.StringValue("/data"), InstanceID: types.StringValue("vm_1")})
		want := client.CreateVolumeRequest{Name: "data", DiskOfferingSlug: "custom", SizeGB: 50, Region: "af-abj", MountPoint: "/data"}
		if req != want {
			t.Fatalf("req = %+v, want %+v: the instance is attached in a separate step, never at creation", req, want)
		}
	})
	t.Run("unset and unknown optionals are omitted", func(t *testing.T) {
		req := toCreateRequest(model{Name: types.StringValue("data"), DiskOfferingSlug: types.StringValue("small-5gb"),
			SizeGB: types.Int64Unknown(), Region: types.StringUnknown(), MountPoint: types.StringNull()})
		if req.SizeGB != 0 || req.Region != "" || req.MountPoint != "" || req.InstanceID != "" {
			t.Fatalf("req = %+v", req)
		}
	})
}

func TestFromAPIResponse(t *testing.T) {
	v := volume("vol_1", "data", "small-5gb", 5, "shared")
	v.AttachedInstanceID = ptr("vm_1")
	v.DesiredInstanceID = ptr("vm_1")
	v.MountPoint = ptr("/data")
	v.SourceSnapshotID = ptr("snap_9")

	m := fromAPIResponse(model{}, &v)
	if m.SourceSnapshotID.ValueString() != "snap_9" {
		t.Fatalf("source_snapshot_id = %v", m.SourceSnapshotID)
	}

	if m.ID.ValueString() != "vol_1" || m.SizeGB.ValueInt64() != 5 || m.StorageType.ValueString() != "shared" || m.Zone.ValueString() != "af-abj-1" ||
		m.MonthlyCostMinor.ValueInt64() != 116800 || m.Currency.ValueString() != "NGN" || m.MountPoint.ValueString() != "/data" {
		t.Fatalf("m = %+v", m)
	}
	if m.InstanceID.ValueString() != "vm_1" {
		t.Fatalf("instance_id = %v", m.InstanceID)
	}
	if m.CreatedAt.ValueString() != "2026-10-04T12:10:12Z" {
		t.Fatalf("created_at = %v, want whole seconds", m.CreatedAt)
	}
}

// A failed attach leaves a desired instance but no attachment. State must show the
// real attachment, otherwise Terraform would believe the volume is attached.
func TestFromAPIResponseUsesTheRealAttachmentNotTheRequestedOne(t *testing.T) {
	v := volume("vol_1", "data", "small-5gb", 5, "shared")
	v.DesiredInstanceID = ptr("vm_1")

	m := fromAPIResponse(model{}, &v)

	if !m.InstanceID.IsNull() {
		t.Fatalf("instance_id = %v: the volume is not attached", m.InstanceID)
	}
}

func TestFromAPIResponseWithoutCostOrOptionals(t *testing.T) {
	v := volume("vol_1", "data", "small-5gb", 5, "shared")
	v.MonthlyCost, v.Region, v.StorageType, v.Zone, v.CreatedAt = nil, nil, nil, nil, nil

	m := fromAPIResponse(model{}, &v)

	if !m.MonthlyCostMinor.IsNull() || !m.Currency.IsNull() || !m.Region.IsNull() || !m.StorageType.IsNull() || !m.Zone.IsNull() || !m.CreatedAt.IsNull() || !m.MountPoint.IsNull() {
		t.Fatalf("m = %+v: missing values must be null, not zero", m)
	}
}

func TestPendingModelHasNoUnknownValues(t *testing.T) {
	plan := model{
		ID: types.StringUnknown(), Name: types.StringValue("data"), DiskOfferingSlug: types.StringValue("small-5gb"), SizeGB: types.Int64Unknown(),
		Region: types.StringUnknown(), InstanceID: types.StringValue("vm_1"), MountPoint: types.StringUnknown(), SourceSnapshotID: types.StringUnknown(),
		StorageType: types.StringUnknown(), Zone: types.StringUnknown(), ObservedState: types.StringUnknown(),
		MonthlyCostMinor: types.Int64Unknown(), Currency: types.StringUnknown(), CreatedAt: types.StringUnknown(), UpdatedAt: types.StringUnknown(),
	}

	m := pendingModel(plan, "vol_9")

	unknowns := map[string]interface{ IsUnknown() bool }{
		"id": m.ID, "size_gb": m.SizeGB, "region": m.Region, "source_snapshot_id": m.SourceSnapshotID, "mount_point": m.MountPoint, "storage_type": m.StorageType, "zone": m.Zone,
		"observed_state": m.ObservedState, "monthly_cost_minor": m.MonthlyCostMinor, "currency": m.Currency, "created_at": m.CreatedAt, "updated_at": m.UpdatedAt,
	}
	for name, v := range unknowns {
		if v.IsUnknown() {
			t.Errorf("%s is unknown in the pending state, so it could not be saved", name)
		}
	}
	if m.ID.ValueString() != "vol_9" {
		t.Fatal("the id must be saved")
	}
	if !m.InstanceID.IsNull() {
		t.Fatalf("instance_id = %v: the volume is not attached at this point", m.InstanceID)
	}
}
