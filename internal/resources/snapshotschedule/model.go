package snapshotschedule

import (
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the resource's attributes to Go. The tfsdk tags tie each field to an
// attribute name in the schema.
type model struct {
	ID             types.String `tfsdk:"id"`
	InstanceID     types.String `tfsdk:"instance_id"`
	VolumeID       types.String `tfsdk:"volume_id"`
	DatabaseID     types.String `tfsdk:"database_id"`
	Frequency      types.String `tfsdk:"frequency"`
	RetentionCount types.Int64  `tfsdk:"retention_count"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	NextRunAt      types.String `tfsdk:"next_run_at"`
}

// fromAPIResponse builds the state from the API schedule. The id is the id of the
// instance, volume or database the schedule belongs to, since a schedule has none of its own.
func fromAPIResponse(s *client.SnapshotSchedule) model {
	id := ""
	switch {
	case s.InstanceID != nil:
		id = *s.InstanceID
	case s.VolumeID != nil:
		id = *s.VolumeID
	case s.DatabaseID != nil:
		id = *s.DatabaseID
	}
	return model{
		ID:             types.StringValue(id),
		InstanceID:     types.StringPointerValue(s.InstanceID),
		VolumeID:       types.StringPointerValue(s.VolumeID),
		DatabaseID:     types.StringPointerValue(s.DatabaseID),
		Frequency:      types.StringValue(s.Frequency),
		RetentionCount: types.Int64Value(s.RetentionCount),
		Enabled:        types.BoolValue(s.Enabled),
		NextRunAt:      timestampValue(s.NextRunAt),
	}
}

// toRequest turns the plan into the full request. Every field is sent, so nothing
// depends on the platform's defaults.
func toRequest(m model) client.PutSnapshotScheduleRequest {
	return client.PutSnapshotScheduleRequest{
		Frequency:      m.Frequency.ValueString(),
		RetentionCount: m.RetentionCount.ValueInt64(),
		Enabled:        m.Enabled.ValueBool(),
	}
}

// isSet reports whether an id attribute holds a value.
func isSet(v types.String) bool { return !v.IsNull() && !v.IsUnknown() }

// timestampValue formats a time to whole seconds, so state stays stable if the
// backend varies the fractional digits, as it does elsewhere.
func timestampValue(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}
