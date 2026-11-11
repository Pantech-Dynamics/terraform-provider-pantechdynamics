package certificates

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

var _ ephemeral.EphemeralResourceWithConfigure = &ExportEphemeral{}

// ExportEphemeral exports a certificate with its private key as an
// ephemeral resource: Terraform keeps the result in memory for the run only,
// never in the plan or state. It shares the export check and the
// descriptions with the data source of the same name.
type ExportEphemeral struct {
	api exportAPI
}

// NewExportEphemeral is the factory the provider registers.
func NewExportEphemeral() ephemeral.EphemeralResource { return &ExportEphemeral{} }

// Metadata sets the type name: pantechdynamics_certificate_export.
func (e *ExportEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate_export"
}

// Schema describes the attributes.
func (e *ExportEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Exports a certificate with its private key, to install it outside Pantech, **without the key ever reaching the plan or state**. " +
			"This is the recommended way to export (Terraform 1.10 and later); the pantechdynamics_certificate_export data source is for older Terraform and keeps the key in state. " +
			"Use the values where ephemeral values are accepted, for example a write-only argument or a provider configuration. " +
			exportRules + " An ephemeral resource is opened on every plan and apply that needs it, so each such run sends one email.",
		Attributes: map[string]schema.Attribute{
			"certificate_id": schema.StringAttribute{
				Description: "Id of the certificate to export (cert_...).", Required: true,
				Validators: []validator.String{resourcekit.IDPrefix("certificate", "cert_")},
			},
			"id":              schema.StringAttribute{Computed: true, Description: "The certificate's id."},
			"certificate_pem": schema.StringAttribute{Computed: true, Description: "The certificate, PEM."},
			"chain_pem":       schema.StringAttribute{Computed: true, Description: "The intermediate certificates, PEM, leaf excluded (may be empty for an imported certificate)."},
			"full_chain_pem":  schema.StringAttribute{Computed: true, Description: "certificate_pem followed by chain_pem, the form most web servers want."},
			"private_key_pem": schema.StringAttribute{Computed: true, Sensitive: true, Description: privateKeyDescription + " Sensitive, and never stored."},
			"not_after":       schema.StringAttribute{Computed: true, Description: "End of validity of the exported certificate, in RFC 3339 UTC, to the second."},
		},
	}
}

// Configure receives the API client from the provider.
func (e *ExportEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(exportAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	e.api = api
}

// Open exports the certificate for this run.
func (e *ExportEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var cfg exportModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, ok := exportChecked(ctx, e.api, cfg.CertificateID, &resp.Diagnostics)
	if ok {
		resp.Diagnostics.Append(resp.Result.Set(ctx, m)...)
	}
}
