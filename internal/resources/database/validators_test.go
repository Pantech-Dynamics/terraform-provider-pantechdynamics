package database

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestPasswordValidator(t *testing.T) {
	tests := map[string]bool{
		"Correct-Horse-Battery-9":    true,
		strings.Repeat("a", 16):      true,
		strings.Repeat("a", 128):     true,
		strings.Repeat("a", 15):      false,
		strings.Repeat("a", 129):     false,
		"has a space in it, 1234567": false,
		`has"double"quote-12345`:     false,
		"has'single'quote-12345":     false,
		`has\backslash-1234567`:      false,
		"non-ascii-é-password-12":    false,
	}
	for pw, wantOK := range tests {
		var resp validator.StringResponse
		passwordValidator{}.ValidateString(context.Background(), validator.StringRequest{Path: pathPassword, ConfigValue: types.StringValue(pw)}, &resp)
		if resp.Diagnostics.HasError() == wantOK {
			t.Errorf("%q: errors = %v", pw, resp.Diagnostics)
		}
		for _, d := range resp.Diagnostics {
			if strings.Contains(d.Detail(), pw) {
				t.Errorf("the diagnostic echoes the password: %s", d.Detail())
			}
		}
	}
}

func TestAdminUsernameValidator(t *testing.T) {
	tests := map[string]bool{
		"dbadmin": true, "admin": true, "app_user1": true,
		"ab": false, "Admin": false, "1admin": false, "root": false, "postgres": false,
		"pg_admin": false, "mysqlx": false, "mariadb1": false, "pantech_admin": false,
		strings.Repeat("a", 33): false,
	}
	for name, wantOK := range tests {
		var resp validator.StringResponse
		adminUsernameValidator{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("admin_username"), ConfigValue: types.StringValue(name)}, &resp)
		if resp.Diagnostics.HasError() == wantOK {
			t.Errorf("%q: errors = %v", name, resp.Diagnostics)
		}
	}
}

func TestAccessRulesValidator(t *testing.T) {
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf("10.%d.0.0/16", i)
	}
	tests := []struct {
		name   string
		cidrs  []string
		wantOK bool
	}{
		{"private ranges", []string{"10.0.0.0/16", "192.168.1.0/24", "203.0.113.7/32"}, true},
		{"empty", []string{}, true},
		{"everything", []string{"0.0.0.0/0"}, false},
		{"wider than /8", []string{"10.0.0.0/7"}, false},
		{"host bits set", []string{"10.0.0.1/16"}, false},
		{"ipv6", []string{"2001:db8::/32"}, false},
		{"not a cidr", []string{"office"}, false},
		{"more than 50", many, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, d := types.SetValueFrom(context.Background(), types.StringType, tt.cidrs)
			if d.HasError() {
				t.Fatal(d)
			}
			var resp validator.SetResponse
			accessRulesValidator{}.ValidateSet(context.Background(), validator.SetRequest{Path: path.Root("access_rules"), ConfigValue: set}, &resp)
			if resp.Diagnostics.HasError() == tt.wantOK {
				t.Errorf("errors = %v", resp.Diagnostics)
			}
			if tt.name == "more than 50" && (len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Summary() != "Too many access rules") {
				t.Errorf("diags = %v, want only the count error", resp.Diagnostics)
			}
		})
	}
}

func TestSecurityGroupIDsValidator(t *testing.T) {
	tests := []struct {
		name   string
		ids    []string
		wantOK bool
	}{
		{"ok", []string{"sg_1", "sg_2"}, true},
		{"none", []string{}, true},
		{"wrong prefix", []string{"vm_1"}, false},
		{"six", []string{"sg_1", "sg_2", "sg_3", "sg_4", "sg_5", "sg_6"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, _ := types.SetValueFrom(context.Background(), types.StringType, tt.ids)
			var resp validator.SetResponse
			securityGroupIDsValidator{}.ValidateSet(context.Background(), validator.SetRequest{Path: path.Root("security_group_ids"), ConfigValue: set}, &resp)
			if resp.Diagnostics.HasError() == tt.wantOK {
				t.Errorf("errors = %v", resp.Diagnostics)
			}
		})
	}
}
