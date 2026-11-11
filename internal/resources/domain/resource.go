// Package domain implements the pantechdynamics_domain resource: a domain name
// registered, and paid for, through Pantech.
package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "dom_"
	kind     = "domain"

	minYears          = 1
	maxYears          = 10
	minCustomNSCount  = 2
	maxCustomNSCount  = 6
	defaultCreateWait = 30 * time.Minute
	defaultUpdateWait = 15 * time.Minute
)

// domainAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type domainAPI interface {
	RegisterDomain(ctx context.Context, req client.RegisterDomainRequest) (*client.DomainOrderResult, error)
	GetDomain(ctx context.Context, id string) (*client.Domain, error)
	ListDomains(ctx context.Context, q string) ([]client.Domain, error)
	UpdateDomain(ctx context.Context, id string, req client.UpdateDomainRequest) (*client.Domain, error)
	SetDomainNameServers(ctx context.Context, id string, req client.SetDomainNameServersRequest) (*client.Domain, error)
	GetDomainContact(ctx context.Context, id string) (*client.DomainContact, error)
	UpdateDomainContact(ctx context.Context, id string, contact client.DomainContact) (*client.Domain, error)
	WaitForDomainOrder(ctx context.Context, id string) (*client.DomainOrder, error)
	ListDomainTLDs(ctx context.Context) ([]client.TLDOffer, error)
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
	ListDomainOrders(ctx context.Context, domainID string) ([]client.DomainOrder, error)
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithModifyPlan     = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one registered domain.
type Resource struct {
	api domainAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource type name: pantechdynamics_domain.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

// warnNoUndo is the line every description and warning about money carries.
const warnNoUndo = "Registering a domain charges your organization's credit and cannot be undone."

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A domain name registered through Pantech. " + warnNoUndo + " " +
			"Create places the order, takes the price from your organization's credit (a failed order returns it) and waits until the registration succeeds. " +
			"When credit does not cover it, the order waits an hour for the difference (in US dollars your saved card is charged it, and a payment link appears only if your bank asks you to confirm the payment; in naira, add credit): the apply stops, and once the order is paid, apply again and the domain is taken into state without a second order. " +
			"**Destroying this resource does not delete, cancel or release the domain**: a registration cannot be undone, so destroy only removes it from Terraform state, and the domain stays in your organization (turn auto_renew off to let it expire). " +
			"With name_servers unset the domain uses Pantech DNS: create a pantechdynamics_dns_zone with the same name to serve its records. Only an API key made by an owner or admin can register or change domains.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the domain, starting with dom_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "The domain to register, in lower case without a trailing dot, such as \"example.com\". Changing it registers another domain (and buys it); the old one stays in your organization.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.DomainName(kind)},
			},
			"years": schema.Int64Attribute{
				Description: "How many years to register for, 1 to 10. Defaults to 1. Used only when the domain is registered: changing it later does nothing (renewals follow auto_renew). " +
					"The price is the TLD's register price plus its renew price for each year after the first (see pantechdynamics_domain_tlds).",
				Optional:   true,
				Validators: []validator.Int64{resourcekit.IntBetween(minYears, maxYears)},
			},
			"contact": contactAttribute(),
			"auto_renew": schema.BoolAttribute{
				Description: "Renew the domain for a year, from your organization's credit, before it expires. Defaults to true.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"transfer_lock": schema.BoolAttribute{
				Description: "Refuse transfers of the domain to another registrar. Defaults to true. Turn it off before transferring the domain away.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"privacy": schema.BoolAttribute{
				Description: "Hide the registrant contact from public registration lookups where the registry allows it. Defaults to true. Always on for .ng domains (.ng, com.ng, org.ng, net.ng, name.ng, mobi.ng, i.ng): the .ng registry hides every registrant's contact details, so false is refused there.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"name_servers": schema.SetAttribute{
				Description: "Custom name servers (2 to 6 host names) to delegate the domain to. Leave unset to use Pantech DNS, which needs a pantechdynamics_dns_zone with the domain's name; removing name_servers points the domain back at that zone.",
				Optional:    true,
				ElementType: types.StringType,
				Validators:  []validator.Set{nameServerList{}},
			},
			"accept_terms": schema.BoolAttribute{
				Description: "Must be true: you accept the registration terms. " + warnNoUndo + " Changing it later does nothing.",
				Required:    true,
				Validators:  []validator.Bool{mustBeTrue{}},
			},
			"tld": schema.StringAttribute{
				Description:   "The domain's top-level domain, such as \"com\".",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Description: "State of the domain: \"registering\", \"active\", \"expired\", \"suspended\", \"transferred_out\" or \"failed\".",
				Computed:    true,
			},
			"name_server_mode": schema.StringAttribute{
				Description: "\"pantech\" when the domain uses Pantech DNS, \"custom\" when it uses name_servers.",
				Computed:    true,
			},
			"current_name_servers": schema.ListAttribute{
				Description:   "The name servers the domain is delegated to now, Pantech DNS's or the custom ones.",
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"zone_id": schema.StringAttribute{
				Description: "Id of the Pantech DNS zone (dnsz_...) the domain is delegated to, or null.",
				Computed:    true,
			},
			"order_id": schema.StringAttribute{
				Description:   "Id of the registration order (dor_...). Null after an import.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"registered_at": schema.StringAttribute{
				Description:   "When the domain was registered, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"expires_at": schema.StringAttribute{
				Description: "When the registration expires, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"contact_verified": schema.BoolAttribute{
				Description: "Whether the registrant clicked the registry's verification email. An unverified registrant is suspended by the registry after 15 days.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

// contactAttribute is the registrant block.
func contactAttribute() schema.SingleNestedAttribute {
	required := func(desc string, v ...validator.String) schema.StringAttribute {
		return schema.StringAttribute{Description: desc, Required: true, Validators: v}
	}
	optional := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Description: desc + " Defaults to \"\".", Optional: true, Computed: true, Default: stringdefault.StaticString("")}
	}
	return schema.SingleNestedAttribute{
		Description: "The registrant: the person or company who holds the domain. It is personal data, so the block is sensitive. " +
			"A change is sent to the registrar. A change made outside Terraform is not detected.",
		Required:  true,
		Sensitive: true,
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{
				Description: "\"person\" or \"company\" (organization_name is then required). Defaults to \"person\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("person"),
				Validators:  []validator.String{resourcekit.OneOf("person", "company")},
			},
			"first_name":        required("First name."),
			"last_name":         required("Last name."),
			"organization_name": optional("The company's name, required when type is \"company\"."),
			"email":             required("Email address. The registry sends a verification email here."),
			"phone":             required("Phone number as +CC.NUMBER, such as \"+234.8012345678\".", phoneFormat{}),
			"address_line1":     required("First address line."),
			"address_line2":     optional("Second address line."),
			"city":              required("City."),
			"state":             optional("State or province."),
			"country_code":      required("Two-letter ISO country code in capitals, such as \"NG\".", countryCode{}),
			"postal_code":       optional("Postal code, at most 20 characters."),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(domainAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ModifyPlan warns, at plan time, before anything irreversible: a create buys
// the domain (with the price, when it can be looked up), and a destroy leaves
// the domain registered.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	switch {
	case req.Plan.Raw.IsNull() && !req.State.Raw.IsNull():
		var state model
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		resp.Diagnostics.AddWarning("Destroy does not release the domain", destroyWarning(state.Name.ValueString()))
	case req.State.Raw.IsNull() && !req.Plan.Raw.IsNull():
		var plan model
		resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
		if resp.Diagnostics.HasError() || plan.Name.IsUnknown() {
			return
		}
		resp.Diagnostics.AddWarning("Registering a domain charges your credit", r.priceWarning(ctx, plan))
	}
}

// priceWarning names the domain and, when the TLD's price can be read, what it
// will cost. It never fails the plan: the price is shown as a courtesy.
func (r *Resource) priceWarning(ctx context.Context, plan model) string {
	name := plan.Name.ValueString()
	years := plan.Years.ValueInt64()
	if plan.Years.IsNull() || plan.Years.IsUnknown() {
		years = 1
	}
	msg := fmt.Sprintf("Applying this plan registers %s for %d year(s). %s If your credit does not cover it, the apply stops with a link to pay the difference by card.", name, years, warnNoUndo)
	if r.api == nil {
		return msg
	}
	tlds, err := r.api.ListDomainTLDs(ctx)
	if err != nil {
		return msg
	}
	if offer, ok := offerFor(tlds, name); ok {
		amount := offer.RegisterMinor + offer.RenewMinor*(years-1)
		msg += fmt.Sprintf(" The listed price is %s %s, before VAT.", offer.Currency, formatMinor(amount))
	}
	return msg
}

// Create places the order, saves the domain straight away, waits for the
// order, then applies the settings the order does not carry.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Create, defaultCreateWait, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	result, err := r.api.RegisterDomain(ctx, toRegisterRequest(plan))
	if client.HasCode(err, client.CodeDomainAlreadyHeld) || client.HasCode(err, client.CodeDomainOrderInFlight) {
		r.adopt(ctx, plan, err, resp)
		return
	}
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error registering domain", err, attributeFor, registerHints)
		return
	}
	plan.OrderID = types.StringValue(result.Order.ID)
	r.save(ctx, plan, &result.Domain, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.api.WaitForDomainOrder(ctx, result.Order.ID); err != nil {
		var orderErr *client.DomainOrderError
		var payErr *client.DomainPaymentRequiredError
		if errors.As(err, &payErr) {
			stopForPayment(ctx, payErr, resp)
			return
		}
		if errors.As(err, &orderErr) {
			// Nothing was registered. Leaving the failed record in state would
			// block the next apply, which should place a new order.
			resp.State.RemoveResource(ctx)
			resp.Diagnostics.AddError("Domain registration failed", orderFailureDetail(orderErr))
			return
		}
		resourcekit.AddWaitError(&resp.Diagnostics, "Error registering domain", kind, result.Domain.ID, err)
		return
	}
	r.converge(ctx, "Error registering domain", plan, result.Domain.ID, &resp.State, &resp.Diagnostics)
}

// adopt handles DOMAIN_ALREADY_HELD: when the domain is already in this
// organization (a create retried after an interrupted apply, or a domain
// bought in the console), it is taken into state instead of failing, and
// nothing is charged again. A domain held elsewhere is an error.
func (r *Resource) adopt(ctx context.Context, plan model, cause error, resp *resource.CreateResponse) {
	d, err := r.findByName(ctx, plan.Name.ValueString())
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error registering domain", cause, registerHints)
		return
	}
	if d.Status != client.DomainActive {
		// An order of this domain may be waiting for its card payment (an
		// apply that stopped for it, run again before paying).
		// A failed lookup is not fatal: adopting goes on and waits for the
		// domain to become active.
		if payErr, err := r.pendingPayment(ctx, d.ID); err == nil && payErr != nil {
			stopForPayment(ctx, payErr, resp)
			return
		}
	}
	resp.Diagnostics.AddWarning("Domain already registered",
		fmt.Sprintf("%s is already in your organization (%s), so it was taken into Terraform state and no new order was placed.", d.Name, d.ID))
	r.save(ctx, plan, d, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	r.converge(ctx, "Error registering domain", plan, d.ID, &resp.State, &resp.Diagnostics)
}

// stopForPayment ends a create whose order waits for its payment (the saved
// card in US dollars, added credit in naira), with the link when the bank
// asks for a confirmation. Nothing is
// kept in state: once paid, the order completes by itself, and the next
// apply finds the domain (or its order still running) and adopts it without
// a second order.
func stopForPayment(ctx context.Context, payErr *client.DomainPaymentRequiredError, resp *resource.CreateResponse) {
	resp.State.RemoveResource(ctx)
	resp.Diagnostics.AddError("Payment needed to register the domain", payErr.Error()+
		".\n\nYour organization's credit does not cover the order (prices are before VAT; VAT is added where it applies). It waits an hour for the difference: in US dollars your saved card is charged it (when a link is shown, your bank asks you to confirm the payment there); in naira, add credit. Then run `terraform apply` again: "+
		"the order completes by itself once the payment arrives, and the next apply takes the domain into state without ordering it again. "+
		"If it is not paid within the hour, the order fails with insufficient_credit and nothing is charged.")
}

// pendingPayment returns the domain's order that waits for a card payment,
// or nil when there is none.
func (r *Resource) pendingPayment(ctx context.Context, domainID string) (*client.DomainPaymentRequiredError, error) {
	orders, err := r.api.ListDomainOrders(ctx, domainID)
	if err != nil {
		return nil, err
	}
	for _, o := range orders {
		if o.Status == client.DomainOrderAwaitingPayment {
			return &client.DomainPaymentRequiredError{Order: o}, nil
		}
	}
	return nil, nil
}

// ValidateConfig checks the .ng rules at plan time: the registry hides every
// .ng registrant, so privacy cannot be off; .ng contacts need a state and a
// postal code; and a name.ng registrant must be a person.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m model
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Name.IsUnknown() || m.Name.IsNull() {
		return
	}
	checkNG(m, &resp.Diagnostics)
}

// Read refreshes state from the API. A domain no longer in the organization
// is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	d, err := r.api.GetDomain(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading domain", err, nil)
		return
	}
	if state.Contact == nil {
		// After an import: read the contact once, so the next plan compares
		// the configuration with it instead of sending it again.
		contact, err := r.api.GetDomainContact(ctx, id)
		if err != nil {
			resp.Diagnostics.AddWarning("Domain contact not read",
				"The registrant contact of "+id+" could not be read, so the next apply sends the configured contact to the registrar. Reading it needs an owner's or admin's API key with the write scope.")
		} else {
			state.Contact = fromAPIContact(contact)
		}
	}
	r.save(ctx, state, d, &resp.State, &resp.Diagnostics)
}

// Update applies what differs: the switches, the name servers and the
// contact. years and accept_terms only matter at registration.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Update, defaultUpdateWait, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	plan.OrderID = state.OrderID
	if plan.Contact != nil && !sameContact(plan.Contact, state.Contact) {
		if _, err := r.api.UpdateDomainContact(ctx, id, toAPIContact(plan.Contact)); err != nil {
			resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating domain contact", err, attributeFor, updateHints)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	r.converge(ctx, "Error updating domain", plan, id, &resp.State, &resp.Diagnostics)
}

// Delete does not delete anything: a registration cannot be undone, and the
// public API has no call that releases a domain. The domain leaves Terraform
// state and stays in the organization.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.AddWarning("Domain removed from Terraform state only", destroyWarning(state.Name.ValueString()))
}

// ImportState adopts a domain of the organization by id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// converge waits until the domain is active, brings its switches and name
// servers to the plan, waits until the registrar applied them, and saves it.
// State is saved on every path, so a failure still records what the domain is.
func (r *Resource) converge(ctx context.Context, summary string, plan model, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	d, err := r.waitFor(ctx, id, "to be active", func(d *client.Domain) bool { return d.Status != "pending" && d.Status != "registering" })
	if err != nil {
		resourcekit.AddWaitError(diags, summary, kind, id, err)
		r.saveLatest(ctx, plan, d, st, diags)
		return
	}
	if d.Status != client.DomainActive {
		diags.AddError(summary, fmt.Sprintf("Domain %s is %q, so its settings cannot change. Contact support and quote the domain id.", id, d.Status))
		r.saveLatest(ctx, plan, d, st, diags)
		return
	}

	if change, ok := settingsChange(plan, d); ok {
		if d, err = r.api.UpdateDomain(ctx, id, change); err != nil {
			resourcekit.AddHintedAPIError(diags, summary, err, attributeFor, updateHints)
			r.refresh(ctx, plan, id, st, diags)
			return
		}
	}
	change, ok, nsDiags := nameServerChange(ctx, plan, d)
	diags.Append(nsDiags...)
	if ok {
		if d, err = r.api.SetDomainNameServers(ctx, id, change); err != nil {
			resourcekit.AddHintedAPIError(diags, summary, err, attributeFor, nameServerHints)
			r.refresh(ctx, plan, id, st, diags)
			return
		}
	}

	settled, err := r.waitFor(ctx, id, "to apply its settings at the registrar", func(d *client.Domain) bool { return !d.SettingsPending })
	if err != nil {
		resourcekit.AddWaitError(diags, summary, kind, id, err)
		settled = d
	}
	r.saveLatest(ctx, plan, settled, st, diags)
}

// waitFor polls the domain until done reports true, and returns the last
// domain it read (nil if none). A domain that failed ends the wait.
func (r *Resource) waitFor(ctx context.Context, id, what string, done func(*client.Domain) bool) (*client.Domain, error) {
	var latest *client.Domain
	err := r.api.WaitUntil(ctx, kind+" "+id+" "+what, func(ctx context.Context) (bool, error) {
		d, err := r.api.GetDomain(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		latest = d
		if d.Status == client.DomainFailed {
			return false, fmt.Errorf("domain %s failed (%s)", id, codeOr(d.FailureCode))
		}
		return done(d), nil
	})
	return latest, err
}

// refresh reads the domain and saves it, after a failed change.
func (r *Resource) refresh(ctx context.Context, plan model, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	d, err := r.api.GetDomain(ctx, id)
	if err != nil {
		return // the change error is already reported; the next refresh fixes state
	}
	r.saveLatest(ctx, plan, d, st, diags)
}

func (r *Resource) saveLatest(ctx context.Context, plan model, d *client.Domain, st *tfsdk.State, diags *diag.Diagnostics) {
	if d != nil {
		r.save(ctx, plan, d, st, diags)
	}
}

func (r *Resource) save(ctx context.Context, prev model, d *client.Domain, st *tfsdk.State, diags *diag.Diagnostics) {
	m, dd := fromAPIResponse(ctx, prev, d)
	diags.Append(dd...)
	if dd.HasError() {
		return
	}
	diags.Append(st.Set(ctx, m)...)
}

// findByName returns the organization's domain with exactly this name.
func (r *Resource) findByName(ctx context.Context, name string) (*client.Domain, error) {
	domains, err := r.api.ListDomains(ctx, name)
	if err != nil {
		return nil, err
	}
	for i := range domains {
		if domains[i].Name == name {
			return &domains[i], nil
		}
	}
	return nil, fmt.Errorf("domain %s: %w", name, client.ErrNotFound)
}

func withTimeout(ctx context.Context, get resourcekit.Timeout, def time.Duration, diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, def)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}

func destroyWarning(name string) string {
	return fmt.Sprintf("A domain registration cannot be undone, so destroying pantechdynamics_domain only removes %s from Terraform state. "+
		"The domain stays registered to your organization and, with auto_renew on, keeps renewing from your credit. "+
		"Turn auto_renew off (in Terraform before destroying, or in the console) to let it expire.", name)
}

// sameContact compares two contact blocks.
func sameContact(a, b *contactModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return toAPIContact(a) == toAPIContact(b)
}

// offerFor finds the offer whose TLD is the longest suffix of name, so
// "example.co.uk" matches "co.uk" before "uk".
func offerFor(tlds []client.TLDOffer, name string) (client.TLDOffer, bool) {
	var best client.TLDOffer
	found := false
	for _, t := range tlds {
		if strings.HasSuffix(name, "."+t.TLD) && (!found || len(t.TLD) > len(best.TLD)) {
			best, found = t, true
		}
	}
	return best, found
}

// formatMinor writes minor units as a decimal amount: 150000 as "1,500.00".
func formatMinor(minor int64) string {
	major, cents := minor/100, minor%100
	digits := fmt.Sprintf("%d", major)
	var b strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return fmt.Sprintf("%s.%02d", b.String(), cents)
}

func codeOr(code *string) string {
	if code == nil || *code == "" {
		return "unknown"
	}
	return *code
}

// orderFailureDetail explains a failed registration order and what to do.
func orderFailureDetail(e *client.DomainOrderError) string {
	msg := e.Error() + "."
	if hint := orderFailureHints[codeOr(e.Order.FailureCode)]; hint != "" {
		msg += "\n\n" + hint
	}
	return msg
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "years", "accept_terms", "name_servers", "auto_renew", "transfer_lock", "privacy":
		return path.Root(field), true
	}
	if sub, ok := strings.CutPrefix(field, "contact."); ok && contactFields[sub] {
		return path.Root("contact").AtName(sub), true
	}
	if field == "contact" {
		return path.Root("contact"), true
	}
	return path.Path{}, false
}

var contactFields = map[string]bool{
	"type": true, "first_name": true, "last_name": true, "organization_name": true, "email": true, "phone": true,
	"address_line1": true, "address_line2": true, "city": true, "state": true, "country_code": true, "postal_code": true,
}

// Hints for the refusals a user can act on.
var (
	registerHints = map[string]string{
		client.CodeDomainNotAvailable:      "The name is taken, premium or reserved. Choose another name.",
		client.CodeDomainAlreadyHeld:       "The domain is already registered on Pantech by another organization.",
		client.CodeTLDNotSold:              "Pantech does not sell this TLD. See the pantechdynamics_domain_tlds data source for the ones it does.",
		client.CodeDomainOrderInFlight:     "Another order for this domain is still running. Run `terraform apply` again once it finishes.",
		client.CodeDomainOrderLimitReached: "Your organization has placed the most domain orders it may in 24 hours (20). Run `terraform apply` again later.",
		"DOMAIN_LOOKUP_UNAVAILABLE":        "Availability cannot be checked right now. Run `terraform apply` again in a few minutes.",
		"INSUFFICIENT_CREDIT":              "Your organization does not have enough credit for this order. Add credit in the console, then run `terraform apply` again.",
		"FORBIDDEN":                        "Only owners and admins can register domains. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":               "Registering a domain needs an API key with the write scope.",
		client.CodeDomainsUnavailable:      "Domains are not sold on this platform yet.",
	}
	updateHints = map[string]string{
		client.CodeDomainNotConfigurable: "The domain is not active, so its settings cannot change.",
		"DOMAIN_LOCKED":                  "Turn transfer_lock off first.",
		"FORBIDDEN":                      "Only owners and admins can change domains. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":             "Changing a domain needs an API key with the write scope.",
		client.CodeDomainsUnavailable:    "Domains are not available on this platform yet.",
	}
	nameServerHints = map[string]string{
		client.CodeDomainZoneMismatch:    "The Pantech DNS zone's name must be the domain's name.",
		client.CodeDNSZoneNotFound:       "To use Pantech DNS, first create a pantechdynamics_dns_zone with the domain's name (and make this resource depend on it), or set name_servers.",
		client.CodeDomainNotConfigurable: "The domain is not active, so its name servers cannot change.",
		"FORBIDDEN":                      "Only owners and admins can change domains. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":             "Changing a domain needs an API key with the write scope.",
	}
	orderFailureHints = map[string]string{
		"insufficient_credit": "Add credit in the console, then run `terraform apply` again to place a new order.",
		"domain_unavailable":  "The name was taken before the order completed. Choose another name.",
		"registrar_rejected":  "The registrar refused the registration. Contact support and quote the order id.",
		"contact_rejected":    "The registrar refused the contact. Check the contact block (phone as +CC.NUMBER, a two-letter country code), then run `terraform apply` again.",
		"tld_not_sold":        "Pantech no longer sells this TLD.",
		"billing_unavailable": "Billing was unavailable. Run `terraform apply` again in a few minutes.",
	}
)
