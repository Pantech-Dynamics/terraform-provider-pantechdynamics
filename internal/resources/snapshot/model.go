package snapshot

import (
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the resource's attributes to Go. The tfsdk tags tie each field to an
// attribute name in the schema.
type model struct {
	ID            types.String   `tfsdk:"id"`
	Name          types.String   `tfsdk:"name"`
	InstanceID    types.String   `tfsdk:"instance_id"`
	VolumeID      types.String   `tfsdk:"volume_id"`
	ObservedState types.String   `tfsdk:"observed_state"`
	Trigger       types.String   `tfsdk:"trigger"`
	SizeBytes     types.Int64    `tfsdk:"size_bytes"`
	CompletedAt   types.String   `tfsdk:"completed_at"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

// fromAPIResponse builds the state from the API object. The source (instance_id or
// volume_id) comes from the API, so an imported snapshot fills in the right one.
func fromAPIResponse(prev model, s *client.Snapshot) model {
	return model{
		ID:            types.StringValue(s.ID),
		Name:          types.StringValue(s.Name),
		InstanceID:    types.StringPointerValue(s.InstanceID),
		VolumeID:      types.StringPointerValue(s.VolumeID),
		ObservedState: types.StringValue(s.ObservedState),
		Trigger:       types.StringValue(s.Trigger),
		SizeBytes:     types.Int64Value(s.SizeBytes),
		CompletedAt:   timestampValue(s.CompletedAt),
		CreatedAt:     timestampValue(s.CreatedAt),
		Timeouts:      prev.Timeouts,
	}
}

// pendingModel is the state saved the moment a create is accepted, before any wait.
// It carries the id, so a timeout or a cancel cannot orphan the snapshot (which is
// billed for storage), and it holds no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	return model{
		ID:            types.StringValue(id),
		Name:          plan.Name,
		InstanceID:    nullIfUnknown(plan.InstanceID),
		VolumeID:      nullIfUnknown(plan.VolumeID),
		ObservedState: types.StringNull(),
		Trigger:       types.StringNull(),
		SizeBytes:     types.Int64Null(),
		CompletedAt:   types.StringNull(),
		CreatedAt:     types.StringNull(),
		Timeouts:      plan.Timeouts,
	}
}

func nullIfUnknown(v types.String) types.String {
	if v.IsUnknown() {
		return types.StringNull()
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
