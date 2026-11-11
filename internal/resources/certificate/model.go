package certificate

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go.
type model struct {
	ID                types.String   `tfsdk:"id"`
	Names             types.List     `tfsdk:"names"`
	KeyType           types.String   `tfsdk:"key_type"`
	Exportable        types.Bool     `tfsdk:"exportable"`
	Description       types.String   `tfsdk:"description"`
	WaitForIssuance   types.Bool     `tfsdk:"wait_for_issuance"`
	CommonName        types.String   `tfsdk:"common_name"`
	Status            types.String   `tfsdk:"status"`
	PendingReason     types.String   `tfsdk:"pending_reason"`
	FailureCode       types.String   `tfsdk:"failure_code"`
	ValidationRecords types.List     `tfsdk:"validation_records"`
	Issuer            types.String   `tfsdk:"issuer"`
	SerialNumber      types.String   `tfsdk:"serial_number"`
	FingerprintSHA256 types.String   `tfsdk:"fingerprint_sha256"`
	NotBefore         types.String   `tfsdk:"not_before"`
	NotAfter          types.String   `tfsdk:"not_after"`
	RenewAfter        types.String   `tfsdk:"renew_after"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

// validationRecord is one DNS record to add at another DNS host so the CA
// can validate a name.
type validationRecord struct {
	Name  types.String `tfsdk:"name"`
	Type  types.String `tfsdk:"type"`
	Value types.String `tfsdk:"value"`
}

var validationRecordType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name": types.StringType, "type": types.StringType, "value": types.StringType,
}}

// toRequest turns the plan into the request.
func toRequest(ctx context.Context, plan model) (client.RequestCertificateRequest, diag.Diagnostics) {
	var names []string
	diags := plan.Names.ElementsAs(ctx, &names, false)
	return client.RequestCertificateRequest{
		Names: names, KeyType: plan.KeyType.ValueString(), Exportable: plan.Exportable.ValueBool(), Description: plan.Description.ValueString(),
	}, diags
}

// validationRecords lists the CNAMEs to add at another DNS host, one per
// challenge name: names that share one (example.com and *.example.com)
// share the record. Names in a Pantech DNS zone need none.
func validationRecords(c *client.Certificate) []validationRecord {
	out := []validationRecord{}
	seen := map[string]bool{}
	for _, v := range c.Validations {
		if v.Method != "cname" || v.CNAMEName == nil || v.CNAMETarget == nil || seen[*v.CNAMEName] {
			continue
		}
		seen[*v.CNAMEName] = true
		out = append(out, validationRecord{Name: types.StringValue(*v.CNAMEName), Type: types.StringValue("CNAME"), Value: types.StringValue(*v.CNAMETarget)})
	}
	return out
}

// fromAPIResponse builds the state. What only Terraform knows (the wait
// switch and the timeouts) is carried over from prev.
func fromAPIResponse(ctx context.Context, prev model, c *client.Certificate) (model, diag.Diagnostics) {
	names, diags := types.ListValueFrom(ctx, types.StringType, nonNil(c.Names))
	records, d := types.ListValueFrom(ctx, validationRecordType, validationRecords(c))
	diags.Append(d...)
	wait := prev.WaitForIssuance
	if wait.IsNull() || wait.IsUnknown() {
		wait = types.BoolValue(true) // after an import: the default
	}
	return model{
		ID:                types.StringValue(c.ID),
		Names:             names,
		KeyType:           types.StringValue(c.KeyType),
		Exportable:        types.BoolValue(c.Exportable),
		Description:       types.StringValue(c.Description),
		WaitForIssuance:   wait,
		CommonName:        types.StringPointerValue(c.CommonName),
		Status:            types.StringValue(c.Status),
		PendingReason:     types.StringPointerValue(c.PendingReason),
		FailureCode:       types.StringPointerValue(c.FailureCode),
		ValidationRecords: records,
		Issuer:            types.StringPointerValue(c.Issuer),
		SerialNumber:      types.StringPointerValue(c.SerialNumber),
		FingerprintSHA256: types.StringPointerValue(c.FingerprintSHA256),
		NotBefore:         resourcekit.Timestamp(c.NotBefore),
		NotAfter:          resourcekit.Timestamp(c.NotAfter),
		RenewAfter:        resourcekit.Timestamp(c.RenewAfter),
		Timeouts:          prev.Timeouts,
	}, diags
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
