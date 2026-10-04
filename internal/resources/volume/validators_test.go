package volume

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestStringChecks(t *testing.T) {
	tests := []struct {
		name    string
		check   stringCheck
		value   types.String
		wantErr bool
	}{
		{"name ok", nameCheck, types.StringValue("data"), false},
		{"name with spaces inside is ok", nameCheck, types.StringValue("my data"), false},
		{"name empty", nameCheck, types.StringValue(""), true},
		{"name blank", nameCheck, types.StringValue("   "), true},
		{"name null", nameCheck, types.StringNull(), false},
		{"mount point ok", mountPointCheck, types.StringValue("/data"), false},
		{"mount point nested", mountPointCheck, types.StringValue("/mnt/data/x"), false},
		{"mount point relative", mountPointCheck, types.StringValue("data"), true},
		{"mount point root only", mountPointCheck, types.StringValue("/"), true},
		{"mount point with space", mountPointCheck, types.StringValue("/my data"), true},
		{"mount point empty", mountPointCheck, types.StringValue(""), true},
		{"mount point unknown", mountPointCheck, types.StringUnknown(), false},
		{"instance id ok", instanceIDCheck, types.StringValue("vm_06gg88kvrxt6z7h6js3h1799xm"), false},
		{"instance id wrong kind", instanceIDCheck, types.StringValue("vol_1"), true},
		{"instance id empty", instanceIDCheck, types.StringValue(""), true},
		{"instance id unknown", instanceIDCheck, types.StringUnknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.StringResponse
			tt.check.ValidateString(ctx, validator.StringRequest{ConfigValue: tt.value}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %v", resp.Diagnostics)
			}
		})
	}
}
