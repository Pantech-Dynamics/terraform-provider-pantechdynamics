package provider

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProviderMetadata(t *testing.T) {
	var resp provider.MetadataResponse
	New("test")().Metadata(context.Background(), provider.MetadataRequest{}, &resp)

	if resp.TypeName != "pantechdynamics" {
		t.Fatalf("TypeName = %q, want pantechdynamics", resp.TypeName)
	}
}

func TestNewClient(t *testing.T) {
	const goodURL = "https://api-dev.example.com/public/v1"

	tests := []struct {
		name      string
		cfg       providerModel
		envURL    string
		envKey    string
		wantAttrs []string // attribute paths expected in error diagnostics
	}{
		{
			name: "from config",
			cfg:  providerModel{BaseURL: types.StringValue(goodURL), APIKey: types.StringValue("PAN_x")},
		},
		{
			name:   "from environment",
			cfg:    providerModel{BaseURL: types.StringNull(), APIKey: types.StringNull()},
			envURL: goodURL, envKey: "PAN_x",
		},
		{
			name:   "config wins over environment",
			cfg:    providerModel{BaseURL: types.StringValue(goodURL), APIKey: types.StringValue("PAN_x")},
			envURL: "not a url", envKey: "PAN_env",
		},
		{
			name:      "nothing set",
			cfg:       providerModel{BaseURL: types.StringNull(), APIKey: types.StringNull()},
			wantAttrs: []string{"base_url", "api_key"},
		},
		{
			name:      "missing key only",
			cfg:       providerModel{BaseURL: types.StringValue(goodURL), APIKey: types.StringNull()},
			wantAttrs: []string{"api_key"},
		},
		{
			name:      "invalid url",
			cfg:       providerModel{BaseURL: types.StringValue("api.example.com"), APIKey: types.StringValue("PAN_x")},
			wantAttrs: []string{"base_url"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envBaseURL, tt.envURL)
			t.Setenv(envAPIKey, tt.envKey)

			c, diags := newClient(tt.cfg, "test")

			if len(tt.wantAttrs) == 0 {
				if diags.HasError() || c == nil {
					t.Fatalf("diags = %v, client = %v", diags, c)
				}
				return
			}
			if c != nil || !diags.HasError() {
				t.Fatalf("want an error, got client %v and diags %v", c, diags)
			}
			var got []string
			for _, d := range diags.Errors() {
				if withPath, ok := d.(diag.DiagnosticWithPath); ok {
					got = append(got, withPath.Path().String())
				}
			}
			if !slices.Equal(got, tt.wantAttrs) {
				t.Fatalf("error attributes = %v, want %v", got, tt.wantAttrs)
			}
		})
	}
}

func TestNewClientErrorsNeverContainAPIKey(t *testing.T) {
	const secret = "PAN_super_secret_value"
	_, diags := newClient(providerModel{BaseURL: types.StringValue("bad url"), APIKey: types.StringValue(secret)}, "test")

	for _, d := range diags {
		if strings.Contains(d.Summary()+d.Detail(), secret) {
			t.Fatalf("diagnostic leaks the API key: %s", d.Detail())
		}
	}
}
