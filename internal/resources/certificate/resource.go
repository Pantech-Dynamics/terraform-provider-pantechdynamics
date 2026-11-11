// Package certificate implements the pantechdynamics_certificate resource: a
// public TLS certificate Pantech requests from a publicly trusted CA and
// renews by itself. It also holds what the certificate resources share:
// name checks and the explanation of a refused delete.
package certificate

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
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
	// IDPrefix starts every certificate id.
	IDPrefix = "cert_"
	kind     = "certificate"

	// defaultCreateWait bounds the wait for issuance. With Pantech DNS a
	// certificate is usually issued within two minutes; CNAMEs at another
	// DNS host can take longer to be seen.
	defaultCreateWait = 20 * time.Minute

	// MaxDescriptionLength is the contract's limit on a description.
	MaxDescriptionLength = 255
)

// certificateAPI is the part of the API client this resource needs.
type certificateAPI interface {
	RequestCertificate(ctx context.Context, req client.RequestCertificateRequest) (*client.Certificate, error)
	GetCertificate(ctx context.Context, id string) (*client.Certificate, error)
	UpdateCertificateDescription(ctx context.Context, id, description string) (*client.Certificate, error)
	DeleteCertificate(ctx context.Context, id string) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one public certificate.
type Resource struct {
	api certificateAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// Metadata sets the type name: pantechdynamics_certificate.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A public TLS certificate, issued free by a publicly trusted CA through Pantech and renewed automatically from 30 days before it expires. " +
			"Names in an active Pantech DNS zone of your organization are validated by Pantech; for any other name, add the CNAME in validation_records at your DNS host once (it keeps renewals automatic while it stays). " +
			"The private key never leaves Pantech unless exportable is true (see pantechdynamics_certificate_export). A certificate a Pantech service uses cannot be deleted. At most 100 certificates per organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the certificate, starting with cert_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"names": schema.ListAttribute{
				Description: "1 to 100 DNS names, in lower case, internationalised names in punycode, wildcards as \"*.example.com\" (the whole left-most label). " +
					"The first is the common name when it is at most 64 characters. Changing them requests a new certificate.",
				Required:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				Validators:    []validator.List{CertificateNames()},
			},
			"key_type": schema.StringAttribute{
				Description: "\"ecdsa-p256\" (the default) or \"rsa-2048\". Changing it requests a new certificate.", Optional: true, Computed: true,
				Default: stringdefault.StaticString("ecdsa-p256"), PlanModifiers: replace,
				Validators: []validator.String{resourcekit.OneOf("ecdsa-p256", "rsa-2048")},
			},
			"exportable": schema.BoolAttribute{
				Description: "Whether the certificate and its private key can be exported (pantechdynamics_certificate_export). Defaults to false: only Pantech services can use it. Set at creation and never changed: changing it requests a new certificate.",
				Optional:    true, Computed: true, Default: booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Description: "A note for yourself, at most 255 characters. Defaults to \"\". Changes in place.", Optional: true, Computed: true,
				Default: stringdefault.StaticString(""), Validators: []validator.String{resourcekit.MaxLength(MaxDescriptionLength)},
			},
			"wait_for_issuance": schema.BoolAttribute{
				Description: "Whether create waits until the certificate is issued, up to the create timeout (default 20 minutes); a certificate still validating then is kept, with a warning. Defaults to true. Set false when names are validated by CNAMEs that this same configuration adds from validation_records at another DNS host: create then returns at once, and the certificate is issued once the CNAMEs are in place.",
				Optional:    true, Computed: true, Default: booldefault.StaticBool(true),
			},
			"common_name": schema.StringAttribute{
				Description:   "The first name, when it is at most 64 characters; null otherwise.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Description: "\"pending_validation\", \"issuing\", \"issued\", \"renewing\" (the current certificate keeps working), \"expired\", \"revoked\" or \"failed\".",
				Computed:    true,
			},
			"pending_reason": schema.StringAttribute{
				Description: "While pending: \"awaiting_cname\", \"awaiting_dns\", \"awaiting_ca\", \"rate_limited\", \"ca_unavailable\" or \"validation_failed\". Null otherwise.",
				Computed:    true,
			},
			"failure_code": schema.StringAttribute{
				Description: "When failed: for example \"validation_timeout\" (the CNAMEs were not in place within 72 hours) or \"caa_forbidden\". Null otherwise.",
				Computed:    true,
			},
			"validation_records": schema.ListNestedAttribute{
				Description: "The DNS records to add at your DNS host for names outside Pantech DNS, one per challenge name (names such as example.com and *.example.com share one). Empty when every name is in a Pantech DNS zone.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":  schema.StringAttribute{Computed: true, Description: "The record's name, such as \"_acme-challenge.api.example.org\"."},
					"type":  schema.StringAttribute{Computed: true, Description: "Always \"CNAME\"."},
					"value": schema.StringAttribute{Computed: true, Description: "The CNAME's target."},
				}},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"issuer":             schema.StringAttribute{Computed: true, Description: "The issuing CA's name, once issued."},
			"serial_number":      schema.StringAttribute{Computed: true, Description: "The certificate's serial number, in hexadecimal, once issued. It changes at every renewal."},
			"fingerprint_sha256": schema.StringAttribute{Computed: true, Description: "SHA-256 of the certificate, lower-case hex, once issued. It changes at every renewal."},
			"not_before":         schema.StringAttribute{Computed: true, Description: "Start of validity, in RFC 3339 UTC, to the second."},
			"not_after":          schema.StringAttribute{Computed: true, Description: "End of validity, in RFC 3339 UTC, to the second. It moves at every renewal."},
			"renew_after":        schema.StringAttribute{Computed: true, Description: "When the next renewal starts, in RFC 3339 UTC, to the second."},
			"timeouts":           timeouts.Attributes(ctx, timeouts.Opts{Create: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(certificateAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create requests the certificate, saves it straight away, and, unless told
// not to, waits until it is issued.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := toRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	cert, err := r.api.RequestCertificate(ctx, body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error requesting certificate", err, attributeFor, requestHints)
		return
	}
	r.save(ctx, plan, cert, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() || !plan.WaitForIssuance.ValueBool() {
		return
	}

	d, timeoutDiags := plan.Timeouts.Create(ctx, defaultCreateWait)
	resp.Diagnostics.Append(timeoutDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	latest, err := r.waitIssued(waitCtx, cert)
	r.save(ctx, plan, latest, &resp.State, &resp.Diagnostics)
	switch {
	case errors.Is(err, errStopped):
		// Failed for good: an error, so the next apply replaces it.
		resp.Diagnostics.AddError("Certificate not issued", issueError(cert.ID, latest, err))
	case err != nil:
		// Still validating: a warning, not an error. An error would taint
		// the certificate, and the next apply would throw away a request
		// that may be issued minutes later for a new one.
		resp.Diagnostics.AddWarning("Certificate not issued yet", issueError(cert.ID, latest, err))
	}
}

// waitIssued polls until the certificate is issued, and returns the last
// certificate it read. A failed, revoked or expired certificate ends the
// wait.
func (r *Resource) waitIssued(ctx context.Context, cert *client.Certificate) (*client.Certificate, error) {
	latest := cert
	err := r.api.WaitUntil(ctx, kind+" "+cert.ID+" to be issued", func(ctx context.Context) (bool, error) {
		c, err := r.api.GetCertificate(ctx, cert.ID)
		if err != nil {
			return false, err
		}
		latest = c
		switch c.Status {
		case client.CertificateIssued, client.CertificateRenewing:
			return true, nil
		case client.CertificateFailed, client.CertificateRevoked, client.CertificateExpired:
			return false, errStopped
		}
		return false, nil
	})
	return latest, err
}

var errStopped = errors.New("the certificate stopped")

// issueError explains why the wait ended: the failure code with what to do,
// or, on a timeout, what it is still waiting for, with the CNAMEs to add.
func issueError(id string, c *client.Certificate, err error) string {
	if errors.Is(err, errStopped) {
		msg := fmt.Sprintf("Certificate %s is %q", id, c.Status)
		if c.FailureCode != nil {
			msg += fmt.Sprintf(" (%s)", *c.FailureCode)
			if hint := failureHints[*c.FailureCode]; hint != "" {
				msg += ". " + hint
			}
		}
		return msg + ". It is saved in state; run `terraform apply` again to replace it with a new request."
	}
	msg := fmt.Sprintf("Stopped waiting for certificate %s: %v.", id, err)
	if c.PendingReason != nil {
		msg += fmt.Sprintf(" It is %s (%s).", c.Status, *c.PendingReason)
	}
	if records := validationRecords(c); len(records) > 0 {
		msg += " Add these CNAMEs at your DNS host:"
		for _, rec := range records {
			msg += fmt.Sprintf("\n  %s CNAME %s", rec.Name.ValueString(), rec.Value.ValueString())
		}
		msg += "\nTo create them in the same configuration, set wait_for_issuance = false."
	}
	return msg + "\nThe certificate is saved in state and is issued by itself once validation succeeds; run `terraform refresh` to see it."
}

// Read refreshes state. A certificate deleted outside Terraform is removed
// from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cert, err := r.api.GetCertificate(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading certificate", err, nil)
		return
	}
	r.save(ctx, state, cert, &resp.State, &resp.Diagnostics)
}

// Update changes the description; wait_for_issuance and the timeouts only
// matter at create.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Description.Equal(state.Description) {
		state.WaitForIssuance, state.Timeouts = plan.WaitForIssuance, plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	cert, err := r.api.UpdateCertificateDescription(ctx, state.ID.ValueString(), plan.Description.ValueString())
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating certificate", err, attributeFor, requestHints)
		return
	}
	r.save(ctx, plan, cert, &resp.State, &resp.Diagnostics)
}

// Delete deletes the certificate. It is refused while a Pantech service uses
// it; the error names what uses it.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	DeleteCertificate(ctx, r.api, state.ID.ValueString(), &resp.Diagnostics)
}

// ImportState adopts a certificate Pantech issues. An imported certificate
// belongs to pantechdynamics_certificate_import.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, IDPrefix, req.ID) {
		return
	}
	if r.api != nil {
		if cert, err := r.api.GetCertificate(ctx, req.ID); err == nil && cert.Kind == client.CertificateKindImported {
			resp.Diagnostics.AddError("Wrong resource type for this certificate",
				req.ID+" is an imported certificate. Import it as pantechdynamics_certificate_import instead.")
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *Resource) save(ctx context.Context, prev model, c *client.Certificate, st *tfsdk.State, diags *diag.Diagnostics) {
	m, d := fromAPIResponse(ctx, prev, c)
	diags.Append(d...)
	if d.HasError() {
		return
	}
	diags.Append(st.Set(ctx, m)...)
}

// deleter is what DeleteCertificate needs.
type deleter interface {
	GetCertificate(ctx context.Context, id string) (*client.Certificate, error)
	DeleteCertificate(ctx context.Context, id string) error
}

// DeleteCertificate deletes a certificate and reports a refusal clearly. A
// certificate already gone counts as success. Shared by both certificate
// resources.
func DeleteCertificate(ctx context.Context, api deleter, id string, diags *diag.Diagnostics) {
	err := api.DeleteCertificate(ctx, id)
	if err == nil || errors.Is(err, client.ErrNotFound) {
		return
	}
	if client.HasCode(err, client.CodeCertificateInUse) {
		detail := err.Error() + "\n\nA Pantech service uses this certificate. Detach it from"
		if cert, getErr := api.GetCertificate(ctx, id); getErr == nil && len(cert.InUseBy) > 0 {
			users := make([]string, 0, len(cert.InUseBy))
			for _, u := range cert.InUseBy {
				users = append(users, strings.ReplaceAll(u.Type, "_", " ")+" "+u.ID)
			}
			detail += " " + strings.Join(users, ", ")
		} else {
			detail += " the services that use it"
		}
		diags.AddError("Certificate in use", detail+" first (in Terraform, change them so they no longer reference it), then run `terraform apply` again.")
		return
	}
	resourcekit.AddHintedError(diags, "Error deleting certificate", err, deleteHints)
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch {
	case field == "names", field == "key_type", field == "description", field == "exportable":
		return path.Root(field), true
	case strings.HasPrefix(field, "names["):
		var i int
		if _, err := fmt.Sscanf(field, "names[%d]", &i); err == nil {
			return path.Root("names").AtListIndex(i), true
		}
		return path.Root("names"), true
	default:
		return path.Path{}, false
	}
}

// Hints for the refusals a user can act on.
var (
	requestHints = map[string]string{
		"CERTIFICATE_LIMIT_REACHED":         "Your organization already has the most certificates it may have (100). Delete one you no longer use.",
		"CERTIFICATE_REQUEST_LIMIT_REACHED": "At most 20 certificates can be requested or imported in 24 hours. Run `terraform apply` again later.",
		"CERTIFICATE_RATE_LIMITED":          "These names were requested 5 times in 7 days, the CA's limit. Run `terraform apply` again after the time the error gives.",
		client.CodeCertificatesUnavailable:  "Certificates are not available on this platform yet.",
		"FORBIDDEN":                         "Only owners and admins can request certificates. Use an API key created by one.",
		"INSUFFICIENT_SCOPE":                "Requesting a certificate needs an API key with the write scope.",
		"ACCESS_DENIED":                     "An access policy refuses this. Ask an owner for the certificates permission.",
	}
	deleteHints = map[string]string{
		"INSUFFICIENT_SCOPE":               "Deleting a certificate needs an API key with the delete scope.",
		"FORBIDDEN":                        "Only owners and admins can delete certificates. Use an API key created by one.",
		"ACCESS_DENIED":                    "An access policy refuses certificates:Delete. Ask an owner for it.",
		client.CodeCertificatesUnavailable: "Certificates are not available on this platform yet.",
	}
	failureHints = map[string]string{
		"validation_timeout":  "The CNAMEs in validation_records were not in place within 72 hours.",
		"caa_forbidden":       "A CAA record at the domain does not allow the CA. Add or change it, then request again.",
		"rejected_identifier": "The CA will not issue for one of the names.",
		"dns_record_limit":    "The Pantech DNS zone is full. Delete records you no longer need.",
		"validation_failed":   "The CA could not validate a name. Check the CNAMEs and the domain's delegation.",
	}
)
