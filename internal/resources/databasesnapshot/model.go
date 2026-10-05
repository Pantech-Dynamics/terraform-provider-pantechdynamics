package databasesnapshot

import (
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go.
type model struct {
	ID            types.String   `tfsdk:"id"`
	DatabaseID    types.String   `tfsdk:"database_id"`
	Name          types.String   `tfsdk:"name"`
	ObservedState types.String   `tfsdk:"observed_state"`
	Trigger       types.String   `tfsdk:"trigger"`
	SizeBytes     types.Int64    `tfsdk:"size_bytes"`
	Region        types.String   `tfsdk:"region"`
	CompletedAt   types.String   `tfsdk:"completed_at"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

// fromAPIResponse builds the state from the API object. database_id comes from
// the API when it reports one, and otherwise from prev (the import id).
func fromAPIResponse(prev model, s *client.Snapshot) model {
	dbID := prev.DatabaseID
	if s.DatabaseID != nil && *s.DatabaseID != "" {
		dbID = types.StringValue(*s.DatabaseID)
	}
	return model{
		ID:            types.StringValue(s.ID),
		DatabaseID:    dbID,
		Name:          types.StringValue(s.Name),
		ObservedState: types.StringValue(s.ObservedState),
		Trigger:       types.StringValue(s.Trigger),
		SizeBytes:     types.Int64Value(s.SizeBytes),
		Region:        resourcekit.OptionalString(s.Region),
		CompletedAt:   resourcekit.Timestamp(s.CompletedAt),
		CreatedAt:     resourcekit.Timestamp(s.CreatedAt),
		Timeouts:      prev.Timeouts,
	}
}

// pendingModel is the state saved the moment the snapshot is accepted. It holds
// the id and no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	return model{
		ID:            types.StringValue(id),
		DatabaseID:    plan.DatabaseID,
		Name:          plan.Name,
		ObservedState: types.StringNull(),
		Trigger:       types.StringNull(),
		SizeBytes:     types.Int64Null(),
		Region:        types.StringNull(),
		CompletedAt:   types.StringNull(),
		CreatedAt:     types.StringNull(),
		Timeouts:      plan.Timeouts,
	}
}
