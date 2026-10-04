package sshkey

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

// --- helpers: build the plan and state values Terraform would hand the resource ---

func testSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	New().Schema(ctx, resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func newTestResource(api keyAPI) *Resource { return &Resource{api: api} }

func attrValues(s schema.Schema, set map[string]tftypes.Value) tftypes.Value {
	typ := s.Type().TerraformType(ctx)
	obj, ok := typ.(tftypes.Object)
	if !ok {
		panic("the schema type is not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, at := range obj.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tftypes.NewValue(typ, vals)
}

func str(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

func unknown() tftypes.Value { return tftypes.NewValue(tftypes.String, tftypes.UnknownValue) }

func emptyState(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
}

// planFor is what Terraform passes to Create: user values set, computed ones unknown.
func planFor(s schema.Schema, name, publicKey string) tfsdk.Plan {
	set := map[string]tftypes.Value{
		"name": str(name), "id": unknown(), "fingerprint": unknown(),
		"created_at": unknown(), "private_key": unknown(), "public_key": unknown(),
	}
	if publicKey != "" {
		set["public_key"] = str(publicKey)
	}
	return tfsdk.Plan{Schema: s, Raw: attrValues(s, set)}
}

func stateWith(s schema.Schema, set map[string]tftypes.Value) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: attrValues(s, set)}
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func errorText(d diag.Diagnostics) string {
	var b strings.Builder
	for _, e := range d.Errors() {
		b.WriteString(e.Summary() + " " + e.Detail() + "\n")
	}
	return b.String()
}

// --- tests ---

func TestSchemaIsValid(t *testing.T) {
	if d := testSchema(t).ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func TestMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	New().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &resp)
	if resp.TypeName != "pantechdynamics_ssh_key" {
		t.Fatalf("TypeName = %q", resp.TypeName)
	}
}

func TestConfigure(t *testing.T) {
	t.Run("nil provider data is ignored", func(t *testing.T) {
		r := &Resource{}
		var resp resource.ConfigureResponse
		r.Configure(ctx, resource.ConfigureRequest{}, &resp)
		if resp.Diagnostics.HasError() || r.api != nil {
			t.Fatalf("diags = %v, api = %v", resp.Diagnostics, r.api)
		}
	})
	t.Run("wrong type is an error", func(t *testing.T) {
		var resp resource.ConfigureResponse
		(&Resource{}).Configure(ctx, resource.ConfigureRequest{ProviderData: "nope"}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("want an error")
		}
	})
	t.Run("a real client satisfies keyAPI", func(t *testing.T) {
		c, err := client.New("https://api.example.com/v1", "PAN_x", "test")
		if err != nil {
			t.Fatal(err)
		}
		r := &Resource{}
		var resp resource.ConfigureResponse
		r.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &resp)
		if resp.Diagnostics.HasError() || r.api == nil {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
	})
}

func TestCreate(t *testing.T) {
	s := testSchema(t)

	t.Run("with a supplied key", func(t *testing.T) {
		api := &fakeAPI{}
		var resp resource.CreateResponse
		resp.State = emptyState(s)

		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "laptop", userKey+"\n")}, &resp)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "sshk_1" || m.Fingerprint.IsNull() || m.CreatedAt.IsNull() {
			t.Fatalf("state = %+v", m)
		}
		if m.PublicKey.ValueString() != userKey+"\n" {
			t.Fatalf("public_key = %q, want the user's own text kept", m.PublicKey.ValueString())
		}
		if !m.PrivateKey.IsNull() {
			t.Fatalf("private_key = %v, want null for a supplied key", m.PrivateKey)
		}
	})

	t.Run("generated keypair saves the one-time private key", func(t *testing.T) {
		var resp resource.CreateResponse
		resp.State = emptyState(s)

		newTestResource(&fakeAPI{}).Create(ctx, resource.CreateRequest{Plan: planFor(s, "gen", "")}, &resp)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.PrivateKey.ValueString() != "PRIVATE-KEY" || m.PublicKey.ValueString() == "" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("name taken suggests import and saves nothing", func(t *testing.T) {
		api := &fakeAPI{createErr: &client.APIError{Status: 409, Code: client.CodeSSHKeyNameTaken, Detail: "taken"}}
		var resp resource.CreateResponse
		resp.State = emptyState(s)

		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "laptop", userKey)}, &resp)

		if !strings.Contains(errorText(resp.Diagnostics), "terraform import") {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
		if !resp.State.Raw.IsNull() {
			t.Fatal("state must stay empty when create fails")
		}
	})

	t.Run("validation errors point at the attribute", func(t *testing.T) {
		api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED",
			Errors: []client.FieldError{{Field: "public_key", Code: "INVALID_SSH_PUBLIC_KEY", Message: "must be a valid OpenSSH public key"}}}}
		var resp resource.CreateResponse
		resp.State = emptyState(s)

		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "laptop", "nonsense")}, &resp)

		errs := resp.Diagnostics.Errors()
		if len(errs) != 1 {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
		withPath, ok := errs[0].(diag.DiagnosticWithPath)
		if !ok || !withPath.Path().Equal(path.Root("public_key")) {
			t.Fatalf("diagnostic = %#v, want it attached to public_key", errs[0])
		}
	})

	t.Run("a lost response is adopted and state is saved", func(t *testing.T) {
		api := &fakeAPI{createErr: errConnReset, createLand: true}
		var resp resource.CreateResponse
		resp.State = emptyState(s)

		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "laptop", userKey)}, &resp)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if getModel(t, resp.State).ID.ValueString() != "sshk_1" || api.creates != 1 {
			t.Fatalf("creates = %d", api.creates)
		}
	})
}

func TestRead(t *testing.T) {
	s := testSchema(t)
	existing := func() map[string]tftypes.Value {
		return map[string]tftypes.Value{
			"id": str("sshk_1"), "name": str("laptop"), "public_key": str(userKey),
			"fingerprint": str("old"), "created_at": str("old"), "private_key": str("SECRET"),
		}
	}
	seeded := func() *fakeAPI {
		return &fakeAPI{keys: []client.SSHKey{{ID: "sshk_1", Name: "laptop", PublicKey: userKey, Fingerprint: "SHA256:new"}}}
	}

	t.Run("refreshes and keeps the private key", func(t *testing.T) {
		resp := resource.ReadResponse{State: stateWith(s, existing())}

		newTestResource(seeded()).Read(ctx, resource.ReadRequest{State: stateWith(s, existing())}, &resp)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.Fingerprint.ValueString() != "SHA256:new" || m.PrivateKey.ValueString() != "SECRET" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("deleted outside terraform is removed from state", func(t *testing.T) {
		resp := resource.ReadResponse{State: stateWith(s, existing())}

		newTestResource(&fakeAPI{}).Read(ctx, resource.ReadRequest{State: stateWith(s, existing())}, &resp)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if !resp.State.Raw.IsNull() {
			t.Fatal("state should be removed on 404")
		}
	})

	t.Run("a server error is reported and state is kept", func(t *testing.T) {
		api := &fakeAPI{getErr: &client.APIError{Status: 500, Code: "INTERNAL", RequestID: "req_7"}}
		resp := resource.ReadResponse{State: stateWith(s, existing())}

		newTestResource(api).Read(ctx, resource.ReadRequest{State: stateWith(s, existing())}, &resp)

		if !strings.Contains(errorText(resp.Diagnostics), "req_7") {
			t.Fatalf("diags = %s, want the request_id", errorText(resp.Diagnostics))
		}
		if resp.State.Raw.IsNull() {
			t.Fatal("state must not be removed on a transient error")
		}
	})
}

func TestDelete(t *testing.T) {
	s := testSchema(t)
	state := func() tfsdk.State {
		return stateWith(s, map[string]tftypes.Value{"id": str("sshk_1"), "name": str("laptop")})
	}

	tests := []struct {
		name      string
		api       *fakeAPI
		wantError bool
	}{
		{"deletes", &fakeAPI{keys: []client.SSHKey{{ID: "sshk_1"}}}, false},
		{"already gone counts as success", &fakeAPI{}, false},
		{"other errors are reported", &fakeAPI{deleteErr: &client.APIError{Status: 403, Code: "INSUFFICIENT_SCOPE"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp resource.DeleteResponse

			newTestResource(tt.api).Delete(ctx, resource.DeleteRequest{State: state()}, &resp)

			if resp.Diagnostics.HasError() != tt.wantError {
				t.Fatalf("diags = %s", errorText(resp.Diagnostics))
			}
			if len(tt.api.deleted) != 1 || tt.api.deleted[0] != "sshk_1" {
				t.Fatalf("deleted = %v", tt.api.deleted)
			}
		})
	}
}

func TestImportState(t *testing.T) {
	s := testSchema(t)

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid", "sshk_06gg7kqr15zn334j7ewpm0hq44", false},
		{"wrong prefix", "key_123", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := resource.ImportStateResponse{State: emptyState(s)}
			resp.State.Raw = attrValues(s, nil)

			newTestResource(&fakeAPI{}).ImportState(ctx, resource.ImportStateRequest{ID: tt.id}, &resp)

			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s", errorText(resp.Diagnostics))
			}
			if !tt.wantErr {
				if got := getModel(t, resp.State).ID.ValueString(); got != tt.id {
					t.Fatalf("id = %q", got)
				}
			}
		})
	}
}

func TestUpdateFailsLoudly(t *testing.T) {
	var resp resource.UpdateResponse
	newTestResource(&fakeAPI{}).Update(ctx, resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must error: the API has no update endpoint")
	}
}
