package instance

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func boolVal(v bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, v) }

// The catalogue pairs ubuntu-24-04 with its encrypted variant.
func encryptedCatalogue() []client.Image {
	return []client.Image{
		{Slug: "ubuntu-24-04"},
		{Slug: "ubuntu-24-04-encrypted", Encrypted: true, EncryptedVariantOf: ptr("ubuntu-24-04")},
	}
}

// encrypted = true on a plain image asks for its encrypted version. The
// instance then reports the variant's own slug, and the state keeps the
// configured one, so the next plan does not replace the instance.
func TestCreateEncryptedFromAPlainImage(t *testing.T) {
	s := testSchema(t)
	api := &fakeAPI{images: encryptedCatalogue()}
	resp := resource.CreateResponse{State: emptyState(s)}
	newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "", "web", map[string]tftypes.Value{"encrypted": boolVal(true)})}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(errorText(resp.Diagnostics))
	}
	if api.lastCreate.Encrypted == nil || !*api.lastCreate.Encrypted {
		t.Fatalf("create sent encrypted = %v, want true", api.lastCreate.Encrypted)
	}
	m := getModel(t, resp.State)
	if m.ImageSlug.ValueString() != "ubuntu-24-04" || !m.Encrypted.ValueBool() {
		t.Fatalf("state image_slug %q encrypted %v; want the configured slug, encrypted", m.ImageSlug.ValueString(), m.Encrypted)
	}

	// A later refresh keeps it too.
	read := resource.ReadResponse{State: resp.State}
	newTestResource(api).Read(ctx, resource.ReadRequest{State: resp.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(errorText(read.Diagnostics))
	}
	if got := getModel(t, read.State).ImageSlug.ValueString(); got != "ubuntu-24-04" {
		t.Fatalf("after a refresh image_slug = %q, want ubuntu-24-04", got)
	}
}

// Left out, encrypted is not sent: the image decides.
func TestCreateWithoutEncryptedSendsNone(t *testing.T) {
	s := testSchema(t)
	api := &fakeAPI{}
	resp := resource.CreateResponse{State: emptyState(s)}
	newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "", "web", map[string]tftypes.Value{
		"encrypted": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
	})}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(errorText(resp.Diagnostics))
	}
	if api.lastCreate.Encrypted != nil {
		t.Fatalf("create sent encrypted = %v, want it left out", *api.lastCreate.Encrypted)
	}
	if m := getModel(t, resp.State); m.Encrypted.ValueBool() || m.DeletionProtect.ValueBool() {
		t.Fatalf("encrypted %v, deletion_protection %v; want both false", m.Encrypted, m.DeletionProtect)
	}
}

// Only the configured image's own encrypted variant is kept as the
// configured slug; any other image reported is drift, and deletion
// protection is read as the API reports it.
func TestReadImageSlugAndDeletionProtection(t *testing.T) {
	s := testSchema(t)
	for _, tc := range []struct {
		name, reported, want string
	}{
		{"its encrypted variant", "ubuntu-24-04-encrypted", "ubuntu-24-04"},
		{"another image", "debian-12-encrypted", "debian-12-encrypted"},
	} {
		inst := running("vm_1", "web")
		inst.ImageSlug, inst.Encrypted, inst.DeletionProtection = tc.reported, true, true
		api := seeded(inst)
		api.images = append(encryptedCatalogue(), client.Image{Slug: "debian-12-encrypted", Encrypted: true, EncryptedVariantOf: ptr("debian-12")})
		state := stateFor(s)
		read := resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: state.Raw}}
		newTestResource(api).Read(ctx, resource.ReadRequest{State: state}, &read)
		if read.Diagnostics.HasError() {
			t.Fatal(errorText(read.Diagnostics))
		}
		m := getModel(t, read.State)
		if m.ImageSlug.ValueString() != tc.want || !m.DeletionProtect.ValueBool() {
			t.Errorf("%s: image_slug %q, deletion_protection %v; want %q, true", tc.name, m.ImageSlug.ValueString(), m.DeletionProtect, tc.want)
		}
	}
}
