// Package dnsrecord implements the pantechdynamics_dns_record resource.
package dnsrecord

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix     = "dnsr_"
	zonePrefix   = "dnsz_"
	kind         = "dns record"
	defaultTTL   = 300
	minTTL       = 60
	maxTTL       = 86400
	maxValues    = 100
	maxCommentLn = 255
)

// recordAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type recordAPI interface {
	CreateDNSRecord(ctx context.Context, zoneID string, req client.CreateDNSRecordRequest) (*client.DNSRecord, error)
	GetDNSRecord(ctx context.Context, zoneID, id string) (*client.DNSRecord, error)
	ListDNSRecords(ctx context.Context, zoneID string, filter client.DNSRecordFilter) ([]client.DNSRecord, error)
	UpdateDNSRecord(ctx context.Context, zoneID, id string, version int64, req client.UpdateDNSRecordRequest) (*client.DNSRecord, error)
	DeleteDNSRecord(ctx context.Context, zoneID, id string) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
	GetDNSZone(ctx context.Context, id string) (*client.DNSZone, error)
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one record set: every value of one name and type in a
// zone.
type Resource struct {
	api recordAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource type name: pantechdynamics_dns_record.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A record set in a Pantech DNS zone: every value of one name and type, such as the A records of www. " +
			"Create and update wait until the change is live on Pantech's name servers (status \"active\"), usually within a minute. In a zone that is not active yet (awaiting delegation) records stay pending until it is: the wait then ends at once with a warning, and the record is kept. " +
			"Names and values are compared the way the DNS server compares them, so \"Mail.Example.com\" and \"mail.example.com.\" are the same value and do not show a change. " +
			"The apex SOA and NS records are managed by Pantech DNS and cannot be managed here. At most 10,000 record sets per zone.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the record set, starting with dnsr_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"zone_id": schema.StringAttribute{
				Description:   "Id of the zone (dnsz_...). Changing it replaces the record set.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.IDPrefix(kind+" zone", zonePrefix)},
			},
			"name": schema.StringAttribute{
				Description: "Name relative to the zone: \"@\" for the zone apex, \"www\", \"*.dev\" for a wildcard, \"_dmarc\". " +
					"A fully-qualified name ending in the zone and a dot (\"www.example.com.\") is accepted too. Changing it replaces the record set.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{relativeName{}},
			},
			"type": schema.StringAttribute{
				Description:   "Record type: " + strings.Join(dnsname.RecordTypes, ", ") + ". NS is for delegating a subdomain only. Changing it replaces the record set.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.OneOf(dnsname.RecordTypes...)},
			},
			"ttl": schema.Int64Attribute{
				Description: "How long resolvers may cache the answer, in seconds, 60 to 86400. Defaults to 300.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(defaultTTL),
				Validators:  []validator.Int64{resourcekit.IntBetween(minTTL, maxTTL)},
			},
			"values": schema.SetAttribute{
				Description: "The answers, 1 to 100. Formats: A \"203.0.113.10\"; AAAA \"2001:db8::1\"; CNAME, NS and PTR a host name; MX \"10 mail.example.com\"; " +
					"SRV \"10 5 5060 sip.example.com\"; CAA `0 issue \"ca.example.net\"`; TXT any printable text up to 4000 bytes (long text is split into 255-byte strings for you). " +
					"A CNAME has exactly one value and cannot be at the apex. Private and carrier-grade NAT addresses need allow_private.",
				Required:    true,
				ElementType: types.StringType,
				Validators:  []validator.Set{valueCount{}},
			},
			"allow_private": schema.BoolAttribute{
				Description: "Allow private and carrier-grade NAT addresses in A and AAAA values, for names that resolve to VPC addresses. Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"comment": schema.StringAttribute{
				Description: "A note for yourself, at most 255 characters. It is not published in DNS. Defaults to \"\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Validators:  []validator.String{resourcekit.MaxLength(maxCommentLn)},
			},
			"fqdn": schema.StringAttribute{
				Description:   "The fully-qualified name, without a trailing dot, such as \"www.example.com\".",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Description: "State of the record set: \"pending\" while a change is applied, \"active\", \"deleting\" or \"failed\".",
				Computed:    true,
			},
			"version": schema.Int64Attribute{
				Description: "Version of the record set. Every update is sent with it as If-Match, so a change made elsewhere since the last refresh is refused instead of overwritten.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(recordAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ValidateConfig checks each value against its record type at plan time, the
// same way the backend checks it.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m model
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateRecord(ctx, m, &resp.Diagnostics)
}

// Create adds the record set, saves it straight away, then waits until it is
// live.
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

	body, diags := toCreateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	zoneID := plan.ZoneID.ValueString()
	created, err := r.api.CreateDNSRecord(ctx, zoneID, body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating DNS record", err, attributeFor, createHints)
		return
	}
	r.save(ctx, plan, created, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitActive(ctx, "Error creating DNS record", plan, created, &resp.State, &resp.Diagnostics)
}

// Read refreshes state from the API. A record set, or its zone, deleted
// outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rec, err := r.api.GetDNSRecord(ctx, state.ZoneID.ValueString(), state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading DNS record", err, nil)
		return
	}
	r.save(ctx, state, rec, &resp.State, &resp.Diagnostics)
}

// Update changes what differs, guarded by the version in state, then waits
// until the change is live.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, plan.Timeouts.Update, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	body, diags := toUpdateRequest(ctx, state, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	zoneID, id := state.ZoneID.ValueString(), state.ID.ValueString()
	if isEmpty(body) {
		// Only the timeouts changed: nothing to send.
		state.Timeouts = plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	updated, err := r.api.UpdateDNSRecord(ctx, zoneID, id, state.Version.ValueInt64(), body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating DNS record", err, attributeFor, updateHints)
		return
	}
	r.waitActive(ctx, "Error updating DNS record", plan, updated, &resp.State, &resp.Diagnostics)
}

// Delete removes the record set and waits until it is gone. One already gone,
// or whose zone is gone, counts as success.
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

	zoneID, id := state.ZoneID.ValueString(), state.ID.ValueString()
	err := r.api.DeleteDNSRecord(ctx, zoneID, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting DNS record", err, deleteHints)
		return
	}
	err = r.api.WaitUntil(ctx, kind+" "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		rec, err := r.api.GetDNSRecord(ctx, zoneID, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if rec.Status == client.DNSRecordFailed {
			return false, failedError(rec)
		}
		return false, nil
	})
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting DNS record", kind, id, err)
	}
}

// ImportState adopts an existing record set by "<zone_id>/<record_id>", or by
// "<zone_id>/<name>/<type>" such as "dnsz_1/www/A" or "dnsz_1/@/MX".
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if (len(parts) != 2 && len(parts) != 3) || !strings.HasPrefix(parts[0], zonePrefix) || (len(parts) == 2 && !strings.HasPrefix(parts[1], idPrefix)) {
		resp.Diagnostics.AddError("Invalid import id",
			fmt.Sprintf("Expected \"<zone_id>/<record_id>\" (such as \"dnsz_1/dnsr_1\") or \"<zone_id>/<name>/<type>\" (such as \"dnsz_1/www/A\"), got %q.", req.ID))
		return
	}
	zoneID, id := parts[0], ""
	if len(parts) == 2 {
		id = parts[1]
	} else {
		rec, err := r.findByNameAndType(ctx, zoneID, parts[1], strings.ToUpper(parts[2]))
		if err != nil {
			resp.Diagnostics.AddError("Cannot import DNS record", err.Error())
			return
		}
		if rec.Managed {
			resp.Diagnostics.AddError("Cannot import DNS record", fmt.Sprintf("The %s record at %s is managed by Pantech DNS and cannot be changed.", rec.Type, rec.FQDN))
			return
		}
		id = rec.ID
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), zoneID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// findByNameAndType finds the record set of one name and type in a zone. The
// name may be written in any form the backend accepts.
func (r *Resource) findByNameAndType(ctx context.Context, zoneID, name, recordType string) (*client.DNSRecord, error) {
	records, err := r.api.ListDNSRecords(ctx, zoneID, client.DNSRecordFilter{Type: recordType})
	if err != nil {
		return nil, err
	}
	for i := range records {
		rec := &records[i]
		if rel, ok := dnsname.RelativeName(zoneOf(rec), name); ok && rec.Type == recordType && rec.Name == rel {
			return rec, nil
		}
	}
	return nil, fmt.Errorf("zone %s has no %s record set named %q", zoneID, recordType, name)
}

// waitActive waits until the record set is live at the version the write
// returned (or later), then saves it. A record set that fails ends the wait
// with its failure code. A record set in a zone that is not active yet
// (awaiting delegation) stays pending until the zone is, by design: that ends
// the wait with a warning, not an error, so the record is kept rather than
// replaced on the next apply. The state is saved on every path.
func (r *Resource) waitActive(ctx context.Context, summary string, plan model, written *client.DNSRecord, st *tfsdk.State, diags *diag.Diagnostics) {
	latest := written
	var zoneStatus string
	err := r.api.WaitUntil(ctx, kind+" "+written.ID+" to be active", func(ctx context.Context) (bool, error) {
		rec, err := r.api.GetDNSRecord(ctx, written.ZoneID, written.ID)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		latest = rec
		switch {
		case rec.Status == client.DNSRecordFailed:
			return false, failedError(rec)
		case rec.Status == client.DNSRecordActive && rec.Version >= written.Version:
			return true, nil
		}
		if z, err := r.api.GetDNSZone(ctx, written.ZoneID); err == nil && z.Status != client.DNSZoneActive {
			zoneStatus = z.Status
			return false, errZoneNotLive
		}
		return false, nil
	})
	switch {
	case errors.Is(err, errZoneNotLive):
		diags.AddWarning("DNS record pending until the zone is active",
			fmt.Sprintf("Record set %s is saved and stays pending while its zone is %q: Pantech DNS publishes the zone, with its records, once the domain's registrar delegates to the zone's name_servers. Nothing else is needed; `terraform refresh` shows it active afterwards.", written.ID, zoneStatus))
	case err != nil:
		resourcekit.AddWaitError(diags, summary, kind, written.ID, err)
	}
	r.save(ctx, plan, latest, st, diags)
}

// errZoneNotLive ends a record wait whose zone is not active yet.
var errZoneNotLive = errors.New("the zone is not active yet")

// save writes the record set to state, keeping prev's spelling of equivalent
// names and values.
func (r *Resource) save(ctx context.Context, prev model, rec *client.DNSRecord, st *tfsdk.State, diags *diag.Diagnostics) {
	m, d := fromAPIResponse(ctx, prev, rec)
	diags.Append(d...)
	if d.HasError() {
		return
	}
	diags.Append(st.Set(ctx, m)...)
}

// failedError explains a record set the DNS network refused.
func failedError(rec *client.DNSRecord) error {
	code := "unknown"
	if rec.FailureCode != nil && *rec.FailureCode != "" {
		code = *rec.FailureCode
	}
	return fmt.Errorf("dns record %s (%s %s) failed: %s. Check the values, then run `terraform apply` again", rec.ID, rec.Type, rec.FQDN, code)
}

// attributeFor maps the fields a 422 can name to schema attributes. values[i]
// points at the values set as a whole: a set has no index.
func attributeFor(field string) (path.Path, bool) {
	switch {
	case field == "name", field == "type", field == "ttl", field == "allow_private", field == "comment", field == "values":
		return path.Root(field), true
	case strings.HasPrefix(field, "values["):
		return path.Root("values"), true
	default:
		return path.Path{}, false
	}
}

// Hints for the refusals a user can act on.
var (
	createHints = map[string]string{
		client.CodeDNSRecordExists:  "The zone already has a record set with this name and type. Import it with `terraform import` as \"<zone_id>/<name>/<type>\", or put all its values in one pantechdynamics_dns_record.",
		"DNS_CNAME_CONFLICT":        "A CNAME must stand alone at its name: remove the other records at this name, or use another name.",
		"DNS_RECORD_READ_ONLY":      "The apex SOA and NS records are managed by Pantech DNS and cannot be changed.",
		"DNS_RECORD_LIMIT_REACHED":  "The zone already holds the most record sets it may (10,000).",
		"DNS_ZONE_SUSPENDED":        "The zone is suspended. Contact support.",
		"DNS_ZONE_NOT_WRITABLE":     "The zone is being deleted or has failed, so records cannot be added to it.",
		client.CodeDNSZoneNotFound:  "The zone does not exist. Check zone_id.",
		"FORBIDDEN":                 "Only owners and admins can change DNS records. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":        "Changing DNS records needs an API key with the write scope.",
		client.CodeDNSUnavailable:   "Pantech DNS is not available on this platform yet.",
		"INSUFFICIENT_DELETE_SCOPE": "This change needs an API key with the delete scope.",
	}
	updateHints = map[string]string{
		client.CodeVersionConflict: "The record set was changed outside Terraform since it was last read. Run `terraform apply` again: the refresh picks up that change first.",
		"DNS_CNAME_CONFLICT":       "A CNAME must stand alone at its name.",
		"DNS_RECORD_READ_ONLY":     "The apex SOA and NS records are managed by Pantech DNS and cannot be changed.",
		"DNS_ZONE_SUSPENDED":       "The zone is suspended. Contact support.",
		"DNS_ZONE_NOT_WRITABLE":    "The zone is being deleted or has failed, so its records cannot change.",
		"FORBIDDEN":                "Only owners and admins can change DNS records. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":       "Changing DNS records needs an API key with the write scope.",
		client.CodeDNSUnavailable:  "Pantech DNS is not available on this platform yet.",
	}
	deleteHints = map[string]string{
		"INSUFFICIENT_DELETE_SCOPE": "Deleting a DNS record needs an API key with the delete scope.",
		"DNS_RECORD_READ_ONLY":      "The apex SOA and NS records are managed by Pantech DNS and cannot be deleted.",
		"DNS_ZONE_SUSPENDED":        "The zone is suspended. Contact support.",
		"FORBIDDEN":                 "Only owners and admins can change DNS records. Use an API key created by one.",
		client.CodeDNSUnavailable:   "Pantech DNS is not available on this platform yet.",
	}
)
