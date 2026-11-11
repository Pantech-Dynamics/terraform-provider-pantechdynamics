package dnszone

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go. NameServers is a types.List
// because it is unknown in the plan of a new zone.
type model struct {
	ID          types.String   `tfsdk:"id"`
	Name        types.String   `tfsdk:"name"`
	Description types.String   `tfsdk:"description"`
	Status      types.String   `tfsdk:"status"`
	FailureCode types.String   `tfsdk:"failure_code"`
	NameServers types.List     `tfsdk:"name_servers"`
	DomainID    types.String   `tfsdk:"domain_id"`
	RecordCount types.Int64    `tfsdk:"record_count"`
	CreatedAt   types.String   `tfsdk:"created_at"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

// toCreateRequest turns the plan into the create request.
func toCreateRequest(plan model) client.CreateDNSZoneRequest {
	return client.CreateDNSZoneRequest{Name: plan.Name.ValueString(), Description: plan.Description.ValueString()}
}

// fromAPIResponse builds the state from the API zone. The timeouts block is
// Terraform's alone, so it is carried over.
func fromAPIResponse(ctx context.Context, z *client.DNSZone, prevTimeouts timeouts.Value) (model, diag.Diagnostics) {
	servers := z.NameServers
	if servers == nil {
		servers = []string{}
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, servers)
	return model{
		ID:          types.StringValue(z.ID),
		Name:        types.StringValue(z.Name),
		Description: types.StringValue(z.Description),
		Status:      types.StringValue(z.Status),
		FailureCode: resourcekit.OptionalString(z.FailureCode),
		NameServers: list,
		DomainID:    resourcekit.OptionalString(z.DomainID),
		RecordCount: types.Int64Value(z.RecordCount),
		CreatedAt:   resourcekit.Timestamp(z.CreatedAt),
		Timeouts:    prevTimeouts,
	}, diags
}
