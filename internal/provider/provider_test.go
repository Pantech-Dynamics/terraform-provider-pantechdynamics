package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
)

func TestProviderMetadata(t *testing.T) {
	var resp provider.MetadataResponse
	New("test")().Metadata(context.Background(), provider.MetadataRequest{}, &resp)

	if resp.TypeName != "pantechdynamics" {
		t.Fatalf("TypeName = %q, want pantechdynamics", resp.TypeName)
	}
}
