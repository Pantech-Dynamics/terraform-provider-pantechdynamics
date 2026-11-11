package accesspolicy

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/accesspolicy"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// configuredStatements returns the statements the configuration or plan
// holds, from the blocks or from policy_json, and whether they are known. A
// policy_json that does not parse is reported and counts as unknown.
func configuredStatements(ctx context.Context, m model) ([]client.PolicyStatement, bool, diag.Diagnostics) {
	if len(m.Statements) > 0 {
		if !accesspolicy.Known(m.Statements) {
			return nil, false, nil
		}
		statements, diags := accesspolicy.ToAPI(ctx, m.Statements)
		return statements, !diags.HasError(), diags
	}
	if m.PolicyJSON.IsNull() || m.PolicyJSON.IsUnknown() {
		return nil, false, nil
	}
	statements, err := accesspolicy.ParseDocument(m.PolicyJSON.ValueString())
	if err != nil {
		var diags diag.Diagnostics
		diags.AddAttributeError(path.Root("policy_json"), "Invalid policy JSON", err.Error())
		return nil, false, diags
	}
	return statements, true, nil
}

// toRequest turns the plan into the policy body: PUT and POST both take the
// whole policy.
func toRequest(ctx context.Context, plan model) (client.IAMPolicyRequest, diag.Diagnostics) {
	statements, _, diags := configuredStatements(ctx, plan)
	return client.IAMPolicyRequest{Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), Statements: statements}, diags
}

// fromAPIResponse builds the state. The policy keeps the form prev used:
// statement blocks, or policy_json (after an import, blocks). Where prev
// says the same thing as the API in another spelling, prev is kept, so the
// next plan shows no change; a real difference takes the API's statements.
func fromAPIResponse(ctx context.Context, prev model, p *client.IAMPolicy) (model, diag.Diagnostics) {
	var diags diag.Diagnostics
	normalized, err := accesspolicy.MarshalDocument(accesspolicy.Normalize(p.Statements))
	if err != nil {
		diags.AddError("Error encoding policy statements", err.Error())
		return model{}, diags
	}
	m := model{
		ID:              types.StringValue(p.ID),
		Name:            types.StringValue(p.Name),
		Description:     types.StringValue(p.Description),
		PolicyJSON:      types.StringValue(normalized),
		AttachmentCount: types.Int64Value(p.AttachmentCount),
		Version:         types.Int64Value(p.Version),
		CreatedAt:       resourcekit.Timestamp(p.CreatedAt),
	}
	prevStatements, known, _ := configuredStatements(ctx, prev)
	same := known && accesspolicy.Equal(prevStatements, p.Statements)

	switch {
	case len(prev.Statements) > 0 && same:
		m.Statements = prev.Statements
	case len(prev.Statements) == 0 && !prev.PolicyJSON.IsNull() && !prev.PolicyJSON.IsUnknown():
		// policy_json form: no blocks, and the configured text when it means
		// what the API holds.
		if same {
			m.PolicyJSON = prev.PolicyJSON
		}
	default:
		m.Statements, diags = accesspolicy.FromAPI(ctx, p.Statements)
	}
	return m, diags
}
