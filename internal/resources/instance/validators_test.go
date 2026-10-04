package instance

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestNameValidator(t *testing.T) {
	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{"simple", types.StringValue("web"), false},
		{"with hyphens", types.StringValue("web-tier-01"), false},
		{"uppercase", types.StringValue("Web01"), false},
		{"single character", types.StringValue("a"), false},
		{"exactly 63", types.StringValue(strings.Repeat("a", 63)), false},
		{"64 is too long", types.StringValue(strings.Repeat("a", 64)), true},
		{"empty", types.StringValue(""), true},
		{"space", types.StringValue("has space"), true},
		{"underscore", types.StringValue("under_score"), true},
		{"dot", types.StringValue("web.example"), true},
		{"leading hyphen", types.StringValue("-web"), true},
		{"trailing hyphen", types.StringValue("web-"), true},
		{"only a hyphen", types.StringValue("-"), true},
		{"non ascii", types.StringValue("café"), true},
		{"null", types.StringNull(), false},
		{"unknown", types.StringUnknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.StringResponse
			nameValidator{}.ValidateString(ctx, validator.StringRequest{ConfigValue: tt.value}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %v", resp.Diagnostics)
			}
		})
	}
}
