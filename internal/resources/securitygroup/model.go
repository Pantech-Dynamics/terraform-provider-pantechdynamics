package securitygroup

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the resource's attributes to Go. The tfsdk tags tie each field to
// an attribute name in the schema.
type model struct {
	ID            types.String   `tfsdk:"id"`
	Name          types.String   `tfsdk:"name"`
	Rules         types.Set      `tfsdk:"rules"`
	ObservedState types.String   `tfsdk:"observed_state"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	UpdatedAt     types.String   `tfsdk:"updated_at"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

// fromAPIResponse builds the state from the API object. prev supplies the
// timeouts block, which only exists in Terraform and never comes from the API.
func fromAPIResponse(ctx context.Context, prev model, sg *client.SecurityGroup) (model, diag.Diagnostics) {
	rules, diags := rulesToSet(ctx, sg.Rules)
	return model{
		ID:            types.StringValue(sg.ID),
		Name:          types.StringValue(sg.Name),
		Rules:         rules,
		ObservedState: types.StringValue(sg.ObservedState),
		CreatedAt:     timestampValue(sg.CreatedAt),
		UpdatedAt:     timestampValue(sg.UpdatedAt),
		Timeouts:      prev.Timeouts,
	}, diags
}

// pendingModel is the state saved the moment the backend accepts a create, before
// the wait. It carries the id, so a timeout or a cancel cannot orphan the group,
// and holds no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	return model{
		ID:            types.StringValue(id),
		Name:          plan.Name,
		Rules:         plan.Rules,
		ObservedState: types.StringNull(),
		CreatedAt:     types.StringNull(),
		UpdatedAt:     types.StringNull(),
		Timeouts:      plan.Timeouts,
	}
}

// timestampValue formats a time to whole seconds. Whole seconds keep state
// stable if the backend varies the fractional digits, as it does for ssh keys.
func timestampValue(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}
