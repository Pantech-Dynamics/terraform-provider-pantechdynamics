package lookups

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// sshKeyAPI is the part of the API client the lookup needs.
type sshKeyAPI interface {
	ListSSHKeys(ctx context.Context) ([]client.SSHKey, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &SSHKeyDataSource{}
	_ datasource.DataSourceWithValidateConfig = &SSHKeyDataSource{}
)

// SSHKeyDataSource looks up one registered SSH key by id or name.
type SSHKeyDataSource struct {
	api sshKeyAPI
}

// NewSSHKey is the factory the provider registers.
func NewSSHKey() datasource.DataSource { return &SSHKeyDataSource{} }

type sshKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	PublicKey   types.String `tfsdk:"public_key"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_ssh_key.
func (d *SSHKeyDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

// Schema describes the attributes.
func (d *SSHKeyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := selectorAttributes("SSH key", "sshk_", true)
	attrs["fingerprint"] = schema.StringAttribute{Computed: true, Description: "SHA256 fingerprint of the key."}
	attrs["public_key"] = schema.StringAttribute{Computed: true, Description: "The public key, in OpenSSH format."}
	attrs["created_at"] = schema.StringAttribute{Computed: true, Description: "When the key was registered, in RFC 3339 UTC, to the second."}
	resp.Schema = schema.Schema{
		Description: "Looks up one SSH key already registered to the account, by id or name, to install it on instances without managing it. A generated private key is never readable again, so it is not available here.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *SSHKeyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configureAPI[sshKeyAPI](req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *SSHKeyDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	validateSelector(ctx, req, "SSH key", &resp.Diagnostics)
}

// Read lists the keys and picks the one asked for.
func (d *SSHKeyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg sshKeyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	keys, err := d.api.ListSSHKeys(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading SSH keys", err.Error())
		return
	}
	id, name := selector(cfg.ID, cfg.Name)
	key, err := lookup.Pick(keys, "SSH key", id, name,
		func(k client.SSHKey) string { return k.ID }, func(k client.SSHKey) string { return k.Name })
	if err != nil {
		resp.Diagnostics.AddError("SSH key not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, sshKeyModel{
		ID: types.StringValue(key.ID), Name: types.StringValue(key.Name), Fingerprint: types.StringValue(key.Fingerprint),
		PublicKey: types.StringValue(key.PublicKey), CreatedAt: resourcekit.Timestamp(key.CreatedAt),
	})...)
}
