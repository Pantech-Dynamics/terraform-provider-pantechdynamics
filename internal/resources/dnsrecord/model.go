package dnsrecord

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
)

// model maps the resource's attributes to Go. Values is a types.Set because
// the order of a record set's values means nothing, and a set can be unknown
// in a plan.
type model struct {
	ID           types.String   `tfsdk:"id"`
	ZoneID       types.String   `tfsdk:"zone_id"`
	Name         types.String   `tfsdk:"name"`
	Type         types.String   `tfsdk:"type"`
	TTL          types.Int64    `tfsdk:"ttl"`
	Values       types.Set      `tfsdk:"values"`
	AllowPrivate types.Bool     `tfsdk:"allow_private"`
	Comment      types.String   `tfsdk:"comment"`
	FQDN         types.String   `tfsdk:"fqdn"`
	Status       types.String   `tfsdk:"status"`
	Version      types.Int64    `tfsdk:"version"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

// valuesOf reads the configured values.
func valuesOf(ctx context.Context, m model) ([]string, diag.Diagnostics) {
	var values []string
	diags := m.Values.ElementsAs(ctx, &values, false)
	return values, diags
}

// toCreateRequest turns the plan into the create request.
func toCreateRequest(ctx context.Context, plan model) (client.CreateDNSRecordRequest, diag.Diagnostics) {
	values, diags := valuesOf(ctx, plan)
	return client.CreateDNSRecordRequest{
		Name:         plan.Name.ValueString(),
		Type:         plan.Type.ValueString(),
		TTL:          plan.TTL.ValueInt64(),
		Values:       values,
		AllowPrivate: plan.AllowPrivate.ValueBool(),
		Comment:      plan.Comment.ValueString(),
	}, diags
}

// toUpdateRequest sends only what changed between state and plan.
func toUpdateRequest(ctx context.Context, state, plan model) (client.UpdateDNSRecordRequest, diag.Diagnostics) {
	var req client.UpdateDNSRecordRequest
	var diags diag.Diagnostics
	if !plan.TTL.Equal(state.TTL) {
		v := plan.TTL.ValueInt64()
		req.TTL = &v
	}
	if !plan.Values.Equal(state.Values) {
		values, d := valuesOf(ctx, plan)
		diags.Append(d...)
		req.Values = &values
	}
	if !plan.AllowPrivate.Equal(state.AllowPrivate) {
		v := plan.AllowPrivate.ValueBool()
		req.AllowPrivate = &v
	}
	if !plan.Comment.Equal(state.Comment) {
		v := plan.Comment.ValueString()
		req.Comment = &v
	}
	return req, diags
}

// isEmpty reports whether an update would send nothing.
func isEmpty(req client.UpdateDNSRecordRequest) bool {
	return req.TTL == nil && req.Values == nil && req.AllowPrivate == nil && req.Comment == nil
}

// fromAPIResponse builds the state from the API record. The backend stores
// names and values in a canonical form (lower case, host names with a
// trailing dot, quoted TXT). Where the previous plan or state says the same
// thing written differently, that spelling is kept, so a configuration of
// "Mail.Example.com" does not show a change on every plan. A real difference,
// such as a value changed outside Terraform, takes the backend's form, and the
// next plan shows it.
func fromAPIResponse(ctx context.Context, prev model, r *client.DNSRecord) (model, diag.Diagnostics) {
	var diags diag.Diagnostics

	name := types.StringValue(r.Name)
	if !prev.Name.IsNull() && !prev.Name.IsUnknown() {
		if rel, ok := dnsname.RelativeName(zoneOf(r), prev.Name.ValueString()); ok && rel == r.Name {
			name = prev.Name
		}
	}

	values := prev.Values
	if !sameValues(ctx, prev, r) {
		stored := r.Values
		if stored == nil {
			stored = []string{}
		}
		var d diag.Diagnostics
		values, d = types.SetValueFrom(ctx, types.StringType, stored)
		diags.Append(d...)
	}

	comment := types.StringValue(r.Comment)
	if !prev.Comment.IsNull() && !prev.Comment.IsUnknown() && strings.TrimSpace(prev.Comment.ValueString()) == r.Comment {
		comment = prev.Comment // the backend trims the comment
	}

	return model{
		ID:           types.StringValue(r.ID),
		ZoneID:       types.StringValue(r.ZoneID),
		Name:         name,
		Type:         types.StringValue(r.Type),
		TTL:          types.Int64Value(r.TTL),
		Values:       values,
		AllowPrivate: types.BoolValue(r.AllowPrivate),
		Comment:      comment,
		FQDN:         types.StringValue(r.FQDN),
		Status:       types.StringValue(r.Status),
		Version:      types.Int64Value(r.Version),
		Timeouts:     prev.Timeouts,
	}, diags
}

// sameValues reports whether the previous values are the stored ones in
// another spelling.
func sameValues(ctx context.Context, prev model, r *client.DNSRecord) bool {
	if prev.Values.IsNull() || prev.Values.IsUnknown() {
		return false
	}
	configured, diags := valuesOf(ctx, prev)
	if diags.HasError() {
		return false
	}
	return dnsname.SameValues(r.Type, configured, r.Values)
}

// zoneOf derives the zone's name from a record: its fqdn without the
// relative name.
func zoneOf(r *client.DNSRecord) string {
	if r.Name == "@" {
		return r.FQDN
	}
	return strings.TrimPrefix(r.FQDN, r.Name+".")
}
