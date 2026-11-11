// Package dnszone implements the pantechdynamics_dns_zone resource.
package dnszone

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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
	idPrefix = "dnsz_"
	kind     = "dns zone"

	// maxDescriptionLength is the contract's limit on a zone description.
	maxDescriptionLength = 255
)

// zoneAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type zoneAPI interface {
	CreateDNSZone(ctx context.Context, req client.CreateDNSZoneRequest) (*client.DNSZone, error)
	GetDNSZone(ctx context.Context, id string) (*client.DNSZone, error)
	UpdateDNSZone(ctx context.Context, id string, req client.UpdateDNSZoneRequest) (*client.DNSZone, error)
	DeleteDNSZone(ctx context.Context, id string) (*client.DNSZone, error)
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
	QuoteDNSZones(ctx context.Context) (*client.DNSZoneQuote, error)
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
	_ resource.ResourceWithModifyPlan  = &Resource{}
)

// Resource manages one hosted zone on Pantech DNS.
type Resource struct {
	api zoneAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource type name: pantechdynamics_dns_zone.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A hosted zone on Pantech DNS, on Pantech's own name servers. Create waits until the zone has its two name servers. " +
			"Point the domain's registrar at name_servers to make the zone active: a domain registered with pantechdynamics_domain is pointed at it for you. " +
			"Records added before then stay pending: the zone is published on the name servers only once the delegation is proven. " +
			"The first 5 live zones of an organization are free; each further one is billed monthly (the plan shows the price when it applies). " +
			"A zone that is not delegated within 14 days is removed (status \"failed\", failure_code \"delegation_timeout\"). " +
			"At most 25 zones per organization, and 10 zone creates in 24 hours. Only an API key made by an owner or admin can change zones.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the zone, starting with dnsz_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "The domain the zone serves, in lower case without a trailing dot, such as \"example.com\". Changing it replaces the zone, and every record in it.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.DomainName(kind)},
			},
			"description": schema.StringAttribute{
				Description: "A note for yourself, at most 255 characters. Defaults to \"\". Changes in place.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Validators:  []validator.String{resourcekit.MaxLength(maxDescriptionLength)},
			},
			"status": schema.StringAttribute{
				Description: "State of the zone: \"pending\" (being created), \"awaiting_delegation\" (waiting for the registrar to point at name_servers), \"active\", \"suspended\", \"deleting\" or \"failed\".",
				Computed:    true,
			},
			"failure_code": schema.StringAttribute{
				Description: "Why the zone failed, for example \"delegation_timeout\". Null otherwise.",
				Computed:    true,
			},
			"name_servers": schema.ListAttribute{
				Description:   "The two Pantech DNS name servers that serve the zone, such as ana-ns.pantechdynamics.com. Set exactly these at the domain's registrar.",
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"domain_id": schema.StringAttribute{
				Description: "Id of the Pantech domain (dom_...) that delegates to this zone, or null.",
				Computed:    true,
			},
			"record_count": schema.Int64Attribute{
				Description: "How many record sets the zone holds, including the managed apex SOA and NS.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the zone was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider. Terraform calls it
// early with no data, so a nil ProviderData is normal and ignored.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(zoneAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ModifyPlan warns, on a create, when the new zone will be billed: the
// organization already uses all its free zones. The price is a courtesy; a
// quote that cannot be read never fails the plan.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || r.api == nil {
		return
	}
	q, err := r.api.QuoteDNSZones(ctx)
	if err != nil || q.LiveZones < q.FreeZones {
		return
	}
	resp.Diagnostics.AddWarning("This DNS zone is billed",
		fmt.Sprintf("Your organization already has %d live zones, and %d are free, so this zone costs %s %s a month (billed by the hour, before VAT).",
			q.LiveZones, q.FreeZones, q.Currency, formatMinor(q.MonthlyPerZoneMinor)))
}

// formatMinor writes minor units as a decimal amount: 50 as "0.50".
func formatMinor(minor int64) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

// Create adds the zone, saves it straight away, then waits until it leaves
// pending, which is when its name servers are known.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, plan.Timeouts.Create, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	created, err := r.api.CreateDNSZone(ctx, toCreateRequest(plan))
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating DNS zone", err, attributeFor, createHints)
		return
	}
	r.save(ctx, created, plan.Timeouts, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	var settled *client.DNSZone
	err = r.api.WaitUntil(ctx, kind+" "+created.ID+" to get its name servers", func(ctx context.Context) (bool, error) {
		z, err := r.api.GetDNSZone(ctx, created.ID)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		if z.Status == client.DNSZoneFailed {
			return false, failedError(z)
		}
		settled = z
		return z.Status != client.DNSZonePending, nil
	})
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating DNS zone", kind, created.ID, err)
		return
	}
	r.save(ctx, settled, plan.Timeouts, &resp.State, &resp.Diagnostics)
}

// Read refreshes state from the API. A zone deleted outside Terraform is
// removed from state so the next plan creates it again.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	z, err := r.api.GetDNSZone(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && z.Status == client.DNSZoneDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading DNS zone", err, nil)
		return
	}
	r.save(ctx, z, state.Timeouts, &resp.State, &resp.Diagnostics)
}

// Update changes the description, the only argument that changes in place.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	z, err := r.api.UpdateDNSZone(ctx, state.ID.ValueString(), client.UpdateDNSZoneRequest{Description: plan.Description.ValueString()})
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating DNS zone", err, attributeFor, updateHints)
		return
	}
	r.save(ctx, z, plan.Timeouts, &resp.State, &resp.Diagnostics)
}

// Delete asks for the zone to be removed and waits until it is gone. A zone
// already gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, state.Timeouts.Delete, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	_, err := r.api.DeleteDNSZone(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting DNS zone", err, deleteHints)
		return
	}
	err = r.api.WaitUntil(ctx, kind+" "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		z, err := r.api.GetDNSZone(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		// A zone that failed is already removed from the DNS network.
		return z.Status == client.DNSZoneDeleted || z.Status == client.DNSZoneFailed, nil
	})
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting DNS zone", kind, id, err)
	}
}

// ImportState adopts an existing zone by id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// save writes the zone to state.
func (r *Resource) save(ctx context.Context, z *client.DNSZone, prevTimeouts timeouts.Value, st *tfsdk.State, diags *diag.Diagnostics) {
	m, d := fromAPIResponse(ctx, z, prevTimeouts)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	diags.Append(st.Set(ctx, m)...)
}

// failedError explains a zone that failed while it was being created.
func failedError(z *client.DNSZone) error {
	code := "unknown"
	if z.FailureCode != nil && *z.FailureCode != "" {
		code = *z.FailureCode
	}
	if code == "provider_limit" {
		return fmt.Errorf("dns zone %s failed (provider_limit): Pantech DNS could not take the zone now, for example because no free name-server pair was available. Run `terraform apply` again later to replace it; if it keeps failing, contact support and quote the zone id", z.ID)
	}
	return fmt.Errorf("dns zone %s failed (%s). Run `terraform apply` again to replace it; if it keeps failing, contact support and quote the zone id", z.ID, code)
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "description":
		return path.Root(field), true
	default:
		return path.Path{}, false
	}
}

// Hints for the refusals a user can act on.
var (
	createHints = map[string]string{
		client.CodeDNSZoneNameTaken:     "Your organization already has a zone with this name. Import it with `terraform import`, or delete it first.",
		"DNS_ZONE_ACTIVE_ELSEWHERE":     "Another organization already serves this name on Pantech DNS. Contact support if the domain is yours.",
		"DNS_ZONE_LIMIT_REACHED":        "Your organization already has the most zones it may have (25). Delete one you no longer use.",
		"DNS_ZONE_CREATE_LIMIT_REACHED": "At most 10 zones can be created in 24 hours. Run `terraform apply` again later.",
		"FORBIDDEN":                     "Only owners and admins can manage DNS zones. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":            "Managing DNS zones needs an API key with the write scope.",
		client.CodeDNSUnavailable:       "Pantech DNS is not available on this platform yet.",
	}
	updateHints = map[string]string{
		"DNS_ZONE_SUSPENDED":      "The zone is suspended. Contact support.",
		"DNS_ZONE_NOT_WRITABLE":   "The zone is being deleted or has failed, so it cannot change.",
		"FORBIDDEN":               "Only owners and admins can manage DNS zones. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":      "Managing DNS zones needs an API key with the write scope.",
		client.CodeDNSUnavailable: "Pantech DNS is not available on this platform yet.",
	}
	deleteHints = map[string]string{
		"DNS_ZONE_IN_USE_BY_DOMAIN": "A Pantech domain is delegated to this zone. Point the domain at other name servers (name_servers on pantechdynamics_domain) before deleting the zone.",
		"INSUFFICIENT_DELETE_SCOPE": "Deleting a zone needs an API key with the delete scope.",
		"FORBIDDEN":                 "Only owners and admins can manage DNS zones. Use an API key created by one.",
		client.CodeDNSUnavailable:   "Pantech DNS is not available on this platform yet.",
	}
)
