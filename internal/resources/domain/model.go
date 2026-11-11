package domain

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go. Contact is a pointer to a
// struct, which the framework maps to a nested object; nil is a null object
// (after an import whose contact could not be read).
type model struct {
	ID                 types.String   `tfsdk:"id"`
	Name               types.String   `tfsdk:"name"`
	Years              types.Int64    `tfsdk:"years"`
	Contact            *contactModel  `tfsdk:"contact"`
	AutoRenew          types.Bool     `tfsdk:"auto_renew"`
	TransferLock       types.Bool     `tfsdk:"transfer_lock"`
	Privacy            types.Bool     `tfsdk:"privacy"`
	NameServers        types.Set      `tfsdk:"name_servers"`
	AcceptTerms        types.Bool     `tfsdk:"accept_terms"`
	TLD                types.String   `tfsdk:"tld"`
	Status             types.String   `tfsdk:"status"`
	NameServerMode     types.String   `tfsdk:"name_server_mode"`
	CurrentNameServers types.List     `tfsdk:"current_name_servers"`
	ZoneID             types.String   `tfsdk:"zone_id"`
	OrderID            types.String   `tfsdk:"order_id"`
	RegisteredAt       types.String   `tfsdk:"registered_at"`
	ExpiresAt          types.String   `tfsdk:"expires_at"`
	ContactVerified    types.Bool     `tfsdk:"contact_verified"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

// contactModel is the registrant. It is personal data: the whole block is
// sensitive, and it is never logged.
type contactModel struct {
	Type             types.String `tfsdk:"type"`
	FirstName        types.String `tfsdk:"first_name"`
	LastName         types.String `tfsdk:"last_name"`
	OrganizationName types.String `tfsdk:"organization_name"`
	Email            types.String `tfsdk:"email"`
	Phone            types.String `tfsdk:"phone"`
	AddressLine1     types.String `tfsdk:"address_line1"`
	AddressLine2     types.String `tfsdk:"address_line2"`
	City             types.String `tfsdk:"city"`
	State            types.String `tfsdk:"state"`
	CountryCode      types.String `tfsdk:"country_code"`
	PostalCode       types.String `tfsdk:"postal_code"`
}

// toAPIContact turns the contact block into the API shape.
func toAPIContact(c *contactModel) client.DomainContact {
	if c == nil {
		return client.DomainContact{}
	}
	return client.DomainContact{
		Type: c.Type.ValueString(), FirstName: c.FirstName.ValueString(), LastName: c.LastName.ValueString(),
		OrganizationName: c.OrganizationName.ValueString(), Email: c.Email.ValueString(), Phone: c.Phone.ValueString(),
		AddressLine1: c.AddressLine1.ValueString(), AddressLine2: c.AddressLine2.ValueString(), City: c.City.ValueString(),
		State: c.State.ValueString(), CountryCode: c.CountryCode.ValueString(), PostalCode: c.PostalCode.ValueString(),
	}
}

// fromAPIContact turns a contact read back after an import into the block.
func fromAPIContact(c *client.DomainContact) *contactModel {
	return &contactModel{
		Type: types.StringValue(c.Type), FirstName: types.StringValue(c.FirstName), LastName: types.StringValue(c.LastName),
		OrganizationName: types.StringValue(c.OrganizationName), Email: types.StringValue(c.Email), Phone: types.StringValue(c.Phone),
		AddressLine1: types.StringValue(c.AddressLine1), AddressLine2: types.StringValue(c.AddressLine2), City: types.StringValue(c.City),
		State: types.StringValue(c.State), CountryCode: types.StringValue(c.CountryCode), PostalCode: types.StringValue(c.PostalCode),
	}
}

// toRegisterRequest turns the plan into the registration order. Auto-renew and
// privacy are always sent, so the configured value applies from the start.
func toRegisterRequest(plan model) client.RegisterDomainRequest {
	autoRenew, privacy := plan.AutoRenew.ValueBool(), plan.Privacy.ValueBool()
	return client.RegisterDomainRequest{
		Name:        plan.Name.ValueString(),
		Years:       plan.Years.ValueInt64(),
		Contact:     toAPIContact(plan.Contact),
		AutoRenew:   &autoRenew,
		Privacy:     &privacy,
		AcceptTerms: plan.AcceptTerms.ValueBool(),
	}
}

// settingsChange is the PATCH that brings the domain's switches to the plan,
// compared with what the domain reports, so drift is corrected too. ok is
// false when nothing differs.
func settingsChange(plan model, d *client.Domain) (client.UpdateDomainRequest, bool) {
	var req client.UpdateDomainRequest
	if b := plan.AutoRenew.ValueBool(); known(plan.AutoRenew) && b != d.AutoRenew {
		req.AutoRenew = &b
	}
	if b := plan.TransferLock.ValueBool(); known(plan.TransferLock) && b != d.TransferLock {
		req.TransferLock = &b
	}
	if b := plan.Privacy.ValueBool(); known(plan.Privacy) && b != d.Privacy {
		req.Privacy = &b
	}
	return req, req.AutoRenew != nil || req.TransferLock != nil || req.Privacy != nil
}

// nameServerChange is the change that points the domain where the plan says:
// custom name servers when name_servers is set, Pantech DNS when it is not.
// ok is false when the domain already points there.
func nameServerChange(ctx context.Context, plan model, d *client.Domain) (client.SetDomainNameServersRequest, bool, diag.Diagnostics) {
	if plan.NameServers.IsUnknown() {
		return client.SetDomainNameServersRequest{}, false, nil
	}
	if plan.NameServers.IsNull() {
		return client.SetDomainNameServersRequest{UsePantechDNS: true}, d.NameServerMode != client.NameServerModePantech, nil
	}
	var want []string
	diags := plan.NameServers.ElementsAs(ctx, &want, false)
	same := d.NameServerMode == client.NameServerModeCustom && dnsname.SameHosts(want, d.NameServers)
	return client.SetDomainNameServersRequest{NameServers: want}, !same, diags
}

// fromAPIResponse builds the state from the API domain. What the API does not
// return (years, the contact, accept_terms, the order id) is carried over
// from prev, the plan or the prior state.
func fromAPIResponse(ctx context.Context, prev model, d *client.Domain) (model, diag.Diagnostics) {
	current := d.NameServers
	if current == nil {
		current = []string{}
	}
	currentList, diags := types.ListValueFrom(ctx, types.StringType, current)

	nameServers, d2 := stateNameServers(ctx, prev, d)
	diags.Append(d2...)

	return model{
		ID:                 types.StringValue(d.ID),
		Name:               types.StringValue(d.Name),
		Years:              prev.Years,
		Contact:            prev.Contact,
		AutoRenew:          types.BoolValue(d.AutoRenew),
		TransferLock:       types.BoolValue(d.TransferLock),
		Privacy:            types.BoolValue(d.Privacy),
		NameServers:        nameServers,
		AcceptTerms:        prev.AcceptTerms,
		TLD:                types.StringValue(d.TLD),
		Status:             types.StringValue(d.Status),
		NameServerMode:     types.StringValue(d.NameServerMode),
		CurrentNameServers: currentList,
		ZoneID:             resourcekit.OptionalString(d.ZoneID),
		OrderID:            unknownToNull(prev.OrderID),
		RegisteredAt:       resourcekit.Timestamp(d.RegisteredAt),
		ExpiresAt:          resourcekit.Timestamp(d.ExpiresAt),
		ContactVerified:    types.BoolValue(d.ContactVerified),
		Timeouts:           prev.Timeouts,
	}, diags
}

// stateNameServers is name_servers as state should hold it: null while the
// domain uses Pantech DNS, the configured spelling while the domain uses the
// same custom servers, and the domain's servers otherwise, so the next plan
// shows a change made outside Terraform.
func stateNameServers(ctx context.Context, prev model, d *client.Domain) (types.Set, diag.Diagnostics) {
	if d.NameServerMode != client.NameServerModeCustom {
		return types.SetNull(types.StringType), nil
	}
	if known(prev.NameServers) && !prev.NameServers.IsNull() {
		var configured []string
		diags := prev.NameServers.ElementsAs(ctx, &configured, false)
		if !diags.HasError() && dnsname.SameHosts(configured, d.NameServers) {
			return prev.NameServers, nil
		}
	}
	return types.SetValueFrom(ctx, types.StringType, d.NameServers)
}

type knowable interface{ IsUnknown() bool }

func known(v knowable) bool { return !v.IsUnknown() }

func unknownToNull(s types.String) types.String {
	if s.IsUnknown() {
		return types.StringNull()
	}
	return s
}
