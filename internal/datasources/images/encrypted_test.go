package images

import (
	"testing"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func encryptedCatalog() *fakeAPI {
	plain := "ubuntu-24-04"
	return &fakeAPI{images: []client.Image{
		{ID: "img_1", Slug: "ubuntu-24-04", Name: "Ubuntu", Version: "24.04 LTS", Zones: []string{"af-abj-1", "af-abj-2"}},
		{ID: "img_2", Slug: "ubuntu-24-04-encrypted", Name: "Ubuntu (encrypted)", Version: "24.04 LTS", Zones: []string{"af-abj-1"},
			Encrypted: true, EncryptedVariantOf: &plain},
	}}
}

func TestReadExposesEncryption(t *testing.T) {
	resp := read(t, encryptedCatalog())
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got model
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	plain, enc := got.Images[0], got.Images[1]
	if plain.Encrypted.ValueBool() || !plain.EncryptedVariantOf.IsNull() {
		t.Errorf("plain image = %+v", plain)
	}
	if !enc.Encrypted.ValueBool() || enc.EncryptedVariantOf.ValueString() != "ubuntu-24-04" {
		t.Errorf("encrypted image = %+v", enc)
	}
}

func TestSingleReadExposesEncryption(t *testing.T) {
	resp := readOne(t, encryptedCatalog(), "ubuntu-24-04-encrypted")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got singleModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	if !got.Encrypted.ValueBool() || got.EncryptedVariantOf.ValueString() != "ubuntu-24-04" {
		t.Errorf("model = %+v", got)
	}
}
