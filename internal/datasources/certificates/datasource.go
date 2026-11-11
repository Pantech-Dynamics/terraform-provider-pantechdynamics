// Package certificates implements the pantechdynamics_certificate data
// source (one certificate, by id or name) and
// pantechdynamics_certificate_export (an exportable certificate with its key).
package certificates

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// lookupAPI is the part of the API client the lookup needs.
type lookupAPI interface {
	GetCertificate(ctx context.Context, id string) (*client.Certificate, error)
	ListCertificates(ctx context.Context, q string) ([]client.Certificate, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &DataSource{}
	_ datasource.DataSourceWithValidateConfig = &DataSource{}
)

// DataSource looks up one certificate.
type DataSource struct {
	api lookupAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Kind              types.String `tfsdk:"kind"`
	Names             types.List   `tfsdk:"names"`
	CommonName        types.String `tfsdk:"common_name"`
	Description       types.String `tfsdk:"description"`
	KeyType           types.String `tfsdk:"key_type"`
	Exportable        types.Bool   `tfsdk:"exportable"`
	Status            types.String `tfsdk:"status"`
	Issuer            types.String `tfsdk:"issuer"`
	SerialNumber      types.String `tfsdk:"serial_number"`
	FingerprintSHA256 types.String `tfsdk:"fingerprint_sha256"`
	NotBefore         types.String `tfsdk:"not_before"`
	NotAfter          types.String `tfsdk:"not_after"`
}

// Metadata sets the type name: pantechdynamics_certificate.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Looks up one certificate, requested or imported, by id or by one of its names, for example to give its id to a load balancer. Failed, revoked and expired certificates are skipped in a lookup by name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Optional: true, Computed: true, Description: "Id of the certificate, starting with cert_. Set exactly one of id and name."},
			"name": schema.StringAttribute{Optional: true, Computed: true, Description: "One of the certificate's names, such as \"example.com\" or \"*.example.com\". " +
				"If more than one usable certificate has it, the lookup fails and lists their ids. Set exactly one of id and name."},
			"kind":               computed("\"public\" (Pantech requests and renews it) or \"imported\"."),
			"names":              schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Every name on the certificate; the first is the common name."},
			"common_name":        computed("The common name, or null."),
			"description":        computed("The certificate's description."),
			"key_type":           computed("The key's type, such as \"ecdsa-p256\"."),
			"exportable":         schema.BoolAttribute{Computed: true, Description: "Whether pantechdynamics_certificate_export can export it."},
			"status":             computed("\"pending_validation\", \"issuing\", \"issued\", \"renewing\", \"expired\", \"revoked\" or \"failed\"."),
			"issuer":             computed("The issuer's name, once issued."),
			"serial_number":      computed("The serial number, in hexadecimal, once issued."),
			"fingerprint_sha256": computed("SHA-256 of the certificate, lower-case hex, once issued."),
			"not_before":         computed("Start of validity, in RFC 3339 UTC, to the second."),
			"not_after":          computed("End of validity, in RFC 3339 UTC, to the second."),
		},
	}
}

// Configure receives the API client from the provider.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(lookupAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// ValidateConfig requires exactly one of id and name.
func (d *DataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	lookup.ValidateIDOrName(ctx, req.Config, "certificate", &resp.Diagnostics)
}

// Read finds the certificate: by id directly, by name among the usable
// certificates that carry it.
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var cert *client.Certificate
	var err error
	if id := cfg.ID.ValueString(); id != "" {
		cert, err = d.api.GetCertificate(ctx, id)
	} else {
		cert, err = d.byName(ctx, cfg.Name.ValueString())
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Certificate not found", err, readHints)
		return
	}
	names, diags := types.ListValueFrom(ctx, types.StringType, cert.Names)
	resp.Diagnostics.Append(diags...)
	name := cfg.Name
	if name.IsNull() || name.IsUnknown() {
		name = types.StringPointerValue(cert.CommonName)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model{
		ID: types.StringValue(cert.ID), Name: name, Kind: types.StringValue(cert.Kind), Names: names, CommonName: types.StringPointerValue(cert.CommonName),
		Description: types.StringValue(cert.Description), KeyType: types.StringValue(cert.KeyType), Exportable: types.BoolValue(cert.Exportable),
		Status: types.StringValue(cert.Status), Issuer: types.StringPointerValue(cert.Issuer), SerialNumber: types.StringPointerValue(cert.SerialNumber),
		FingerprintSHA256: types.StringPointerValue(cert.FingerprintSHA256), NotBefore: resourcekit.Timestamp(cert.NotBefore), NotAfter: resourcekit.Timestamp(cert.NotAfter),
	})...)
}

// byName returns the one usable certificate carrying the name.
func (d *DataSource) byName(ctx context.Context, name string) (*client.Certificate, error) {
	certs, err := d.api.ListCertificates(ctx, name)
	if err != nil {
		return nil, err
	}
	var matches []client.Certificate
	for _, c := range certs {
		usable := c.Status != client.CertificateFailed && c.Status != client.CertificateRevoked && c.Status != client.CertificateExpired
		if usable && slices.Contains(c.Names, name) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no usable certificate has the name %q", name)
	case 1:
		return &matches[0], nil
	}
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.ID)
	}
	return nil, fmt.Errorf("%d certificates have the name %q (%s). Look it up by id instead", len(matches), name, strings.Join(ids, ", "))
}

// readHints explain the refusals a read can meet.
var readHints = map[string]string{
	client.CodeCertificateNotFound:     "No certificate has this id in your organization.",
	client.CodeCertificatesUnavailable: "Certificates are not available on this platform yet.",
}
