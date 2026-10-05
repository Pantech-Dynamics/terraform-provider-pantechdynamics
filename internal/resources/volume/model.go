package volume

import (
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the resource's attributes to Go. The tfsdk tags tie each field to an
// attribute name in the schema.
type model struct {
	ID               types.String   `tfsdk:"id"`
	Name             types.String   `tfsdk:"name"`
	DiskOfferingSlug types.String   `tfsdk:"disk_offering_slug"`
	SizeGB           types.Int64    `tfsdk:"size_gb"`
	Region           types.String   `tfsdk:"region"`
	InstanceID       types.String   `tfsdk:"instance_id"`
	SourceSnapshotID types.String   `tfsdk:"source_snapshot_id"`
	MountPoint       types.String   `tfsdk:"mount_point"`
	StorageType      types.String   `tfsdk:"storage_type"`
	Zone             types.String   `tfsdk:"zone"`
	ObservedState    types.String   `tfsdk:"observed_state"`
	MonthlyCostMinor types.Int64    `tfsdk:"monthly_cost_minor"`
	Currency         types.String   `tfsdk:"currency"`
	CreatedAt        types.String   `tfsdk:"created_at"`
	UpdatedAt        types.String   `tfsdk:"updated_at"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

// toCreateRequest turns the plan into an API request. The volume is always
// created standalone: the create-time instance_id did nothing on staging, so the
// attach is a separate step. Unset optionals are omitted.
func toCreateRequest(m model) client.CreateVolumeRequest {
	return client.CreateVolumeRequest{
		Name:             m.Name.ValueString(),
		DiskOfferingSlug: m.DiskOfferingSlug.ValueString(),
		SizeGB:           knownInt(m.SizeGB),
		Region:           knownString(m.Region),
		MountPoint:       knownString(m.MountPoint),
	}
}

func knownString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func knownInt(v types.Int64) int64 {
	if v.IsNull() || v.IsUnknown() {
		return 0
	}
	return v.ValueInt64()
}

// fromAPIResponse builds the state from the API object. instance_id is what the
// volume is actually attached to, never what it was asked to attach to: a failed
// attach leaves a desired instance but no attachment. prev supplies only the
// timeouts block, which exists in Terraform alone.
func fromAPIResponse(prev model, v *client.Volume) model {
	m := model{
		ID:               types.StringValue(v.ID),
		Name:             types.StringValue(v.Name),
		DiskOfferingSlug: types.StringValue(v.DiskOfferingSlug),
		SizeGB:           types.Int64Value(v.SizeGB),
		Region:           types.StringPointerValue(v.Region),
		InstanceID:       types.StringPointerValue(v.AttachedInstanceID),
		SourceSnapshotID: types.StringPointerValue(v.SourceSnapshotID),
		MountPoint:       types.StringPointerValue(v.MountPoint),
		StorageType:      types.StringPointerValue(v.StorageType),
		Zone:             types.StringPointerValue(v.Zone),
		ObservedState:    types.StringValue(v.ObservedState),
		MonthlyCostMinor: types.Int64Null(),
		Currency:         types.StringNull(),
		CreatedAt:        timestampValue(v.CreatedAt),
		UpdatedAt:        timestampValue(v.UpdatedAt),
		Timeouts:         prev.Timeouts,
	}
	if v.MonthlyCost != nil {
		m.MonthlyCostMinor = types.Int64Value(v.MonthlyCost.AmountMinor)
		m.Currency = types.StringValue(v.MonthlyCost.Currency)
	}
	return m
}

// pendingModel is the state saved the moment a create is accepted, before any
// wait. It carries the id, so a timeout or a cancel cannot orphan the volume, and
// it holds no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	return model{
		ID:               types.StringValue(id),
		Name:             plan.Name,
		DiskOfferingSlug: plan.DiskOfferingSlug,
		SizeGB:           nullIfUnknownInt(plan.SizeGB),
		Region:           nullIfUnknown(plan.Region),
		InstanceID:       types.StringNull(), // not attached yet
		SourceSnapshotID: nullIfUnknown(plan.SourceSnapshotID),
		MountPoint:       nullIfUnknown(plan.MountPoint),
		StorageType:      types.StringNull(),
		Zone:             types.StringNull(),
		ObservedState:    types.StringNull(),
		MonthlyCostMinor: types.Int64Null(),
		Currency:         types.StringNull(),
		CreatedAt:        types.StringNull(),
		UpdatedAt:        types.StringNull(),
		Timeouts:         plan.Timeouts,
	}
}

func nullIfUnknown(v types.String) types.String {
	if v.IsUnknown() {
		return types.StringNull()
	}
	return v
}

func nullIfUnknownInt(v types.Int64) types.Int64 {
	if v.IsUnknown() {
		return types.Int64Null()
	}
	return v
}

// timestampValue formats a time to whole seconds, so state stays stable if the
// backend varies the fractional digits, as it does elsewhere.
func timestampValue(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}
