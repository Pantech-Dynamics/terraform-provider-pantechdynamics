package certificates

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// exportAPI is the part of the API client the export needs.
type exportAPI interface {
	GetCertificate(ctx context.Context, id string) (*client.Certificate, error)
	ExportCertificate(ctx context.Context, id string) (*client.ExportedCertificate, error)
}

var _ datasource.DataSourceWithConfigure = &ExportDataSource{}

// ExportDataSource exports a certificate with its private key.
type ExportDataSource struct {
	api exportAPI
}

// NewExport is the factory the provider registers.
func NewExport() datasource.DataSource { return &ExportDataSource{} }

type exportModel struct {
	CertificateID  types.String `tfsdk:"certificate_id"`
	ID             types.String `tfsdk:"id"`
	CertificatePEM types.String `tfsdk:"certificate_pem"`
	ChainPEM       types.String `tfsdk:"chain_pem"`
	FullChainPEM   types.String `tfsdk:"full_chain_pem"`
	PrivateKeyPEM  types.String `tfsdk:"private_key_pem"`
	NotAfter       types.String `tfsdk:"not_after"`
}

// Metadata sets the type name: pantechdynamics_certificate_export.
func (d *ExportDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate_export"
}

// Schema describes the attributes.
func (d *ExportDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Exports a certificate with its private key, to install it outside Pantech. " +
			"**Prefer the pantechdynamics_certificate_export ephemeral resource (Terraform 1.10 and later)**: it never writes the key to the plan or state. This data source is for older Terraform. " +
			exportRules + " " +
			"**With this data source the private key is kept in Terraform state** (as a sensitive value): treat the state as a secret. " +
			"A data source is read on every plan and refresh, so **every plan and refresh exports again and emails your owners and admins**; keep it in a configuration you apply rarely.",
		Attributes: map[string]schema.Attribute{
			"certificate_id": schema.StringAttribute{
				Description: "Id of the certificate to export (cert_...).", Required: true,
				Validators: []validator.String{resourcekit.IDPrefix("certificate", "cert_")},
			},
			"id":              schema.StringAttribute{Computed: true, Description: "The certificate's id."},
			"certificate_pem": schema.StringAttribute{Computed: true, Description: "The certificate, PEM."},
			"chain_pem":       schema.StringAttribute{Computed: true, Description: "The intermediate certificates, PEM, leaf excluded (may be empty for an imported certificate)."},
			"full_chain_pem":  schema.StringAttribute{Computed: true, Description: "certificate_pem followed by chain_pem, the form most web servers want."},
			"private_key_pem": schema.StringAttribute{Computed: true, Sensitive: true, Description: privateKeyDescription + " Sensitive, and kept in state."},
			"not_after":       schema.StringAttribute{Computed: true, Description: "End of validity of the exported certificate, in RFC 3339 UTC, to the second."},
		},
	}
}

// Configure receives the API client from the provider.
func (d *ExportDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(exportAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// Read checks the certificate can be exported, then exports it.
func (d *ExportDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg exportModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, ok := exportChecked(ctx, d.api, cfg.CertificateID, &resp.Diagnostics)
	if ok {
		resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
	}
}

// exportChecked checks the certificate can be exported, then exports it.
// The check comes first so a certificate that cannot be exported never
// reaches the export call, which owners and admins are emailed about. Both
// the data source and the ephemeral resource use it.
func exportChecked(ctx context.Context, api exportAPI, certificateID types.String, diags *diag.Diagnostics) (exportModel, bool) {
	id := certificateID.ValueString()
	cert, err := api.GetCertificate(ctx, id)
	if err != nil {
		resourcekit.AddHintedError(diags, "Error reading certificate", err, readHints)
		return exportModel{}, false
	}
	if !cert.Exportable {
		diags.AddAttributeError(pathCertificateID, "Certificate not exportable",
			id+" was created without exportable = true, which cannot change afterwards. Request a new certificate with exportable = true to export it.")
		return exportModel{}, false
	}
	out, err := api.ExportCertificate(ctx, id)
	if err != nil {
		resourcekit.AddHintedError(diags, "Error exporting certificate", err, exportHints)
		return exportModel{}, false
	}
	full := out.CertificatePEM
	if chain := strings.TrimSpace(out.ChainPEM); chain != "" {
		full = strings.TrimRight(full, "\n") + "\n" + out.ChainPEM
	}
	return exportModel{
		CertificateID: certificateID, ID: types.StringValue(id), CertificatePEM: types.StringValue(out.CertificatePEM),
		ChainPEM: types.StringValue(out.ChainPEM), FullChainPEM: types.StringValue(full), PrivateKeyPEM: types.StringValue(out.PrivateKeyPEM),
		NotAfter: resourcekit.Timestamp(out.NotAfter),
	}, true
}

// Descriptions shared by the data source and the ephemeral resource.
const (
	exportRules = "Only a certificate created with exportable = true can be exported, and only once it is issued, with an owner's or admin's API key that has the delete scope. " +
		"**Every export is recorded and emails your organization's owners and admins a security notice**, so each Terraform run that exports sends one. A renewed certificate is exported again on the next run."
	privateKeyDescription = "The private key, PEM (PKCS #8 for certificates Pantech issued)."
)

// exportHints explain the refusals an export can meet.
var exportHints = map[string]string{
	client.CodeCertificateNotIssued:     "The certificate is not issued yet. Apply again once its status is \"issued\" (set wait_for_issuance on pantechdynamics_certificate).",
	client.CodeCertificateNotExportable: "The certificate was created without exportable = true, which cannot change afterwards.",
	"INSUFFICIENT_SCOPE":                "Exporting a certificate needs an API key with the delete scope: it hands out the private key.",
	"FORBIDDEN":                         "Only owners and admins can export certificates. Use an API key created by one.",
	"ACCESS_DENIED":                     "An access policy refuses certificates:Export. Ask an owner for it.",
}

var pathCertificateID = path.Root("certificate_id")
