package sshkey

import (
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the resource's attributes to Go. The tfsdk tags are like Jackson's
// @JsonProperty: they tie each field to an attribute name in the schema.
type model struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	PublicKey   types.String `tfsdk:"public_key"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	CreatedAt   types.String `tfsdk:"created_at"`
	PrivateKey  types.String `tfsdk:"private_key"`
}

// toCreateRequest turns the plan into an API request. An unset public_key is
// omitted so the backend generates the keypair.
func toCreateRequest(m model) client.CreateSSHKeyRequest {
	req := client.CreateSSHKeyRequest{Name: m.Name.ValueString()}
	if !m.PublicKey.IsNull() && !m.PublicKey.IsUnknown() {
		req.PublicKey = m.PublicKey.ValueString()
	}
	return req
}

// fromAPIResponse builds the new state from an API object. prev is the plan or
// the previous state, and it matters for two attributes the API cannot give
// back faithfully:
//   - private_key is returned only once, at creation, so it is carried over.
//   - public_key keeps the user's own text when it is the same key, so a
//     trailing newline or comment difference does not cause a permanent diff.
func fromAPIResponse(prev model, k *client.SSHKey) model {
	m := model{
		ID:          types.StringValue(k.ID),
		Name:        types.StringValue(k.Name),
		PublicKey:   types.StringValue(k.PublicKey),
		Fingerprint: types.StringValue(k.Fingerprint),
		CreatedAt:   createdAtValue(k.CreatedAt),
		PrivateKey:  prev.PrivateKey,
	}

	if !prev.PublicKey.IsNull() && !prev.PublicKey.IsUnknown() && samePublicKey(prev.PublicKey.ValueString(), k.PublicKey) {
		m.PublicKey = prev.PublicKey
	}
	if k.PrivateKey != "" {
		m.PrivateKey = types.StringValue(k.PrivateKey)
	}
	if m.PrivateKey.IsUnknown() {
		m.PrivateKey = types.StringNull()
	}
	return m
}

func createdAtValue(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339Nano))
}

// samePublicKey compares the key type and base64 body only. The trailing
// comment and surrounding whitespace do not identify a key.
func samePublicKey(a, b string) bool {
	fa, fb := strings.Fields(a), strings.Fields(b)
	return len(fa) >= 2 && len(fb) >= 2 && fa[0] == fb[0] && fa[1] == fb[1]
}
