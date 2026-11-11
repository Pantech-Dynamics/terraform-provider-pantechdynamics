// Package certificateimport implements the pantechdynamics_certificate_import
// resource: a certificate and key you bring, for Pantech services to use.
package certificateimport

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resources/certificate"
)

// importAPI is the part of the API client this resource needs.
type importAPI interface {
	ImportCertificate(ctx context.Context, req client.ImportCertificateRequest) (*client.Certificate, error)
	ReimportCertificate(ctx context.Context, id string, req client.ReimportCertificateRequest) (*client.Certificate, error)
	GetCertificate(ctx context.Context, id string) (*client.Certificate, error)
	UpdateCertificateDescription(ctx context.Context, id, description string) (*client.Certificate, error)
	DeleteCertificate(ctx context.Context, id string) error
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource manages one imported certificate.
type Resource struct {
	api importAPI
	now func() time.Time
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{now: time.Now} }

type model struct {
	ID                types.String `tfsdk:"id"`
	CertificatePEM    types.String `tfsdk:"certificate_pem"`
	ChainPEM          types.String `tfsdk:"chain_pem"`
	PrivateKeyPEM     types.String `tfsdk:"private_key_pem"`
	Exportable        types.Bool   `tfsdk:"exportable"`
	Description       types.String `tfsdk:"description"`
	Names             types.List   `tfsdk:"names"`
	CommonName        types.String `tfsdk:"common_name"`
	KeyType           types.String `tfsdk:"key_type"`
	Status            types.String `tfsdk:"status"`
	Issuer            types.String `tfsdk:"issuer"`
	SerialNumber      types.String `tfsdk:"serial_number"`
	FingerprintSHA256 types.String `tfsdk:"fingerprint_sha256"`
	NotBefore         types.String `tfsdk:"not_before"`
	NotAfter          types.String `tfsdk:"not_after"`
}

// Metadata sets the type name: pantechdynamics_certificate_import.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate_import"
}

// Schema describes the attributes.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "A certificate and private key you bring, for Pantech services such as load balancers to use. Pantech never renews it: change the PEMs (for the same names) and apply to replace it in place. " +
			"**The private key is kept in Terraform state** (as a sensitive value): treat the state as a secret. A certificate a Pantech service uses cannot be deleted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the certificate, starting with cert_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"certificate_pem": schema.StringAttribute{
				Description: "The certificate, PEM. Changing it re-imports in place; the new certificate must have the same names.",
				Required:    true,
			},
			"chain_pem": schema.StringAttribute{
				Description: "The intermediate certificates, PEM, leaf excluded. Defaults to \"\". Changing it re-imports in place.",
				Optional:    true, Computed: true, Default: stringdefault.StaticString(""),
			},
			"private_key_pem": schema.StringAttribute{
				Description: "The certificate's private key, PEM (PKCS #8, PKCS #1 or EC), not encrypted. Sensitive, and kept in state. Changing it re-imports in place.",
				Required:    true, Sensitive: true,
			},
			"exportable": schema.BoolAttribute{
				Description: "Whether the certificate and key can be exported again (pantechdynamics_certificate_export). Defaults to false. Changing it imports a new certificate.",
				Optional:    true, Computed: true, Default: booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Description: "A note for yourself, at most 255 characters. Defaults to \"\".", Optional: true, Computed: true,
				Default: stringdefault.StaticString(""), Validators: []validator.String{resourcekit.MaxLength(certificate.MaxDescriptionLength)},
			},
			"names":              schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "The DNS names the certificate is for, read from it."},
			"common_name":        computed("The certificate's common name, or null."),
			"key_type":           computed("The key's type: \"ecdsa-p256\", \"ecdsa-p384\", \"rsa-2048\", \"rsa-3072\" or \"rsa-4096\"."),
			"status":             computed("\"issued\", or \"expired\" once not_after has passed (Pantech does not renew an imported certificate)."),
			"issuer":             computed("The issuer's name as written in the certificate."),
			"serial_number":      computed("The serial number, in hexadecimal."),
			"fingerprint_sha256": computed("SHA-256 of the certificate, lower-case hex. A re-import made outside Terraform shows as a change, which the next apply sets back."),
			"not_before":         computed("Start of validity, in RFC 3339 UTC, to the second."),
			"not_after":          computed("End of validity, in RFC 3339 UTC, to the second."),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(importAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// ValidateConfig checks the PEMs at plan time, as the API would.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m model
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !known(m.CertificatePEM) || !known(m.PrivateKeyPEM) || m.ChainPEM.IsUnknown() {
		return
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	for attr, problem := range Problems(m.CertificatePEM.ValueString(), m.ChainPEM.ValueString(), m.PrivateKeyPEM.ValueString(), now()) {
		resp.Diagnostics.AddAttributeError(path.Root(attr), "Invalid certificate", problem)
	}
}

func known(s types.String) bool { return !s.IsNull() && !s.IsUnknown() }

// Create imports the certificate.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cert, err := r.api.ImportCertificate(ctx, client.ImportCertificateRequest{
		CertificatePEM: plan.CertificatePEM.ValueString(), ChainPEM: plan.ChainPEM.ValueString(), PrivateKeyPEM: plan.PrivateKeyPEM.ValueString(),
		Exportable: plan.Exportable.ValueBool(), Description: plan.Description.ValueString(),
	})
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error importing certificate", err, attributeFor, hints)
		return
	}
	r.save(ctx, plan, cert, &resp.State, &resp.Diagnostics)
}

// Read refreshes state. The PEMs cannot be read back; when the certificate
// the API holds is not the one in state (re-imported outside Terraform),
// certificate_pem is cleared so the next plan sets it back.
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
	if known(state.CertificatePEM) && cert.FingerprintSHA256 != nil {
		if fp, err := Fingerprint(state.CertificatePEM.ValueString()); err == nil && fp != *cert.FingerprintSHA256 {
			state.CertificatePEM = types.StringValue("")
		}
	}
	r.save(ctx, state, cert, &resp.State, &resp.Diagnostics)
}

// Update re-imports when a PEM changed (for the same names) and changes the
// description. After an import of the resource, the configured PEMs are the
// ones the API holds; that is recognised by fingerprint and nothing is sent.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	var cert *client.Certificate
	var err error
	if r.needsReimport(plan, state) {
		cert, err = r.api.ReimportCertificate(ctx, id, client.ReimportCertificateRequest{
			CertificatePEM: plan.CertificatePEM.ValueString(), ChainPEM: plan.ChainPEM.ValueString(), PrivateKeyPEM: plan.PrivateKeyPEM.ValueString(),
		})
		if err != nil {
			resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error re-importing certificate", err, attributeFor, hints)
			return
		}
	}
	if !plan.Description.Equal(state.Description) {
		if cert, err = r.api.UpdateCertificateDescription(ctx, id, plan.Description.ValueString()); err != nil {
			resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating certificate", err, attributeFor, hints)
			return
		}
	}
	if cert == nil {
		if cert, err = r.api.GetCertificate(ctx, id); err != nil {
			resourcekit.AddAPIError(&resp.Diagnostics, "Error reading certificate", err, nil)
			return
		}
	}
	r.save(ctx, plan, cert, &resp.State, &resp.Diagnostics)
}

// needsReimport reports whether the PEMs changed, other than a planned PEM
// that is the very certificate the API already holds (after an import, or
// a whitespace-only change).
func (r *Resource) needsReimport(plan, state model) bool {
	if plan.CertificatePEM.Equal(state.CertificatePEM) && plan.ChainPEM.Equal(state.ChainPEM) && plan.PrivateKeyPEM.Equal(state.PrivateKeyPEM) {
		return false
	}
	if !state.CertificatePEM.IsNull() || state.FingerprintSHA256.IsNull() {
		return true
	}
	fp, err := Fingerprint(plan.CertificatePEM.ValueString())
	return err != nil || fp != state.FingerprintSHA256.ValueString()
}

// Delete deletes the certificate; refused while a Pantech service uses it.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	certificate.DeleteCertificate(ctx, r.api, state.ID.ValueString(), &resp.Diagnostics)
}

// ImportState adopts an imported certificate by id. The PEMs cannot be read
// back; the configured ones are recognised by fingerprint on the next apply.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, "certificate", certificate.IDPrefix, req.ID) {
		return
	}
	if r.api != nil {
		if cert, err := r.api.GetCertificate(ctx, req.ID); err == nil && cert.Kind == client.CertificateKindPublic {
			resp.Diagnostics.AddError("Wrong resource type for this certificate",
				req.ID+" is a certificate Pantech issues. Import it as pantechdynamics_certificate instead.")
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *Resource) save(ctx context.Context, prev model, c *client.Certificate, st *tfsdk.State, diags *diag.Diagnostics) {
	names, d := types.ListValueFrom(ctx, types.StringType, nonNil(c.Names))
	diags.Append(d...)
	m := prev
	m.ID = types.StringValue(c.ID)
	m.Exportable, m.Description = types.BoolValue(c.Exportable), types.StringValue(c.Description)
	m.Names, m.CommonName, m.KeyType, m.Status = names, types.StringPointerValue(c.CommonName), types.StringValue(c.KeyType), types.StringValue(c.Status)
	m.Issuer, m.SerialNumber, m.FingerprintSHA256 = types.StringPointerValue(c.Issuer), types.StringPointerValue(c.SerialNumber), types.StringPointerValue(c.FingerprintSHA256)
	m.NotBefore, m.NotAfter = resourcekit.Timestamp(c.NotBefore), resourcekit.Timestamp(c.NotAfter)
	if m.ChainPEM.IsNull() || m.ChainPEM.IsUnknown() {
		m.ChainPEM = types.StringNull()
	}
	diags.Append(st.Set(ctx, m)...)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "certificate_pem", "chain_pem", "private_key_pem", "description", "exportable":
		return path.Root(field), true
	default:
		return path.Path{}, false
	}
}

// hints explain the refusals a user can act on.
var hints = map[string]string{
	client.CodeCertificateNamesMismatch: "A re-import must be for the same names. For other names, replace the resource (terraform apply -replace).",
	"CERTIFICATE_LIMIT_REACHED":         "Your organization already has the most certificates it may have (100). Delete one you no longer use.",
	"CERTIFICATE_REQUEST_LIMIT_REACHED": "At most 20 certificates can be requested or imported in 24 hours. Run `terraform apply` again later.",
	client.CodeCertificatesUnavailable:  "Certificates are not available on this platform yet.",
	"FORBIDDEN":                         "Only owners and admins can import certificates. Use an API key created by one.",
	"INSUFFICIENT_SCOPE":                "Importing a certificate needs an API key with the write scope.",
	"ACCESS_DENIED":                     "An access policy refuses certificates:Import. Ask an owner for it.",
}
