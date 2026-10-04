package resourcekit_test

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

func TestNameValidator(t *testing.T) {
	tests := map[string]struct {
		in      types.String
		wantErr bool
	}{
		"ok":       {types.StringValue("main"), false},
		"empty":    {types.StringValue(""), true},
		"max":      {types.StringValue(strings.Repeat("a", 255)), false},
		"too long": {types.StringValue(strings.Repeat("a", 256)), true},
		"null":     {types.StringNull(), false},
		"unknown":  {types.StringUnknown(), false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var resp validator.StringResponse
			resourcekit.NameLength("network", 255).ValidateString(Ctx, validator.StringRequest{Path: path.Root("name"), ConfigValue: tt.in}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Errorf("error = %v, want %v", resp.Diagnostics.HasError(), tt.wantErr)
			}
		})
	}
}

func TestCIDRValidator(t *testing.T) {
	tests := map[string]struct {
		in      string
		wantErr bool
	}{
		"ok":           {"10.0.0.0/16", false},
		"host bits":    {"10.0.0.5/16", true},
		"no prefix":    {"10.0.0.0", true},
		"ipv6":         {"fd00::/64", true},
		"garbage":      {"not-a-cidr", true},
		"single host":  {"10.0.0.1/32", false},
		"out of range": {"10.0.0.0/33", true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var resp validator.StringResponse
			resourcekit.IPv4CIDR().ValidateString(Ctx, validator.StringRequest{Path: path.Root("cidr"), ConfigValue: types.StringValue(tt.in)}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Errorf("error = %v, want %v", resp.Diagnostics.HasError(), tt.wantErr)
			}
		})
	}
}

func TestOneOf(t *testing.T) {
	v := resourcekit.OneOf("tcp", "udp")
	for in, wantErr := range map[string]bool{"tcp": false, "udp": false, "icmp": true, "": true} {
		var resp validator.StringResponse
		v.ValidateString(Ctx, validator.StringRequest{Path: path.Root("protocol"), ConfigValue: types.StringValue(in)}, &resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%q: error = %v, want %v", in, resp.Diagnostics.HasError(), wantErr)
		}
	}
	var resp validator.StringResponse
	v.ValidateString(Ctx, validator.StringRequest{ConfigValue: types.StringUnknown()}, &resp)
	if resp.Diagnostics.HasError() {
		t.Error("unknown must be skipped")
	}
}

func TestIntBetween(t *testing.T) {
	v := resourcekit.IntBetween(1, 65535)
	for in, wantErr := range map[int64]bool{1: false, 65535: false, 0: true, 65536: true} {
		var resp validator.Int64Response
		v.ValidateInt64(Ctx, validator.Int64Request{Path: path.Root("port"), ConfigValue: types.Int64Value(in)}, &resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%d: error = %v, want %v", in, resp.Diagnostics.HasError(), wantErr)
		}
	}
	var resp validator.Int64Response
	v.ValidateInt64(Ctx, validator.Int64Request{ConfigValue: types.Int64Null()}, &resp)
	if resp.Diagnostics.HasError() {
		t.Error("null must be skipped")
	}
}

func TestIDPrefix(t *testing.T) {
	v := resourcekit.IDPrefix("network", "net_")
	for in, wantErr := range map[string]bool{"net_1": false, "vm_1": true, "": true} {
		var resp validator.StringResponse
		v.ValidateString(Ctx, validator.StringRequest{Path: path.Root("network_id"), ConfigValue: types.StringValue(in)}, &resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%q: error = %v, want %v", in, resp.Diagnostics.HasError(), wantErr)
		}
	}
	var resp validator.StringResponse
	v.ValidateString(Ctx, validator.StringRequest{ConfigValue: types.StringUnknown()}, &resp)
	if resp.Diagnostics.HasError() {
		t.Error("unknown must be skipped (a reference is not resolved yet)")
	}
}
