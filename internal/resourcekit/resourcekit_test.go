package resourcekit_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

func TestTimestampTruncatesToSeconds(t *testing.T) {
	ts := time.Date(2026, 10, 4, 15, 0, 1, 987654321, time.UTC)
	if got := resourcekit.Timestamp(&ts).ValueString(); got != "2026-10-04T15:00:01Z" {
		t.Errorf("got %s", got)
	}
	if !resourcekit.Timestamp(nil).IsNull() {
		t.Error("nil time should be null")
	}
}

func TestOptionalValues(t *testing.T) {
	empty, set := "", "x"
	if !resourcekit.OptionalString(nil).IsNull() || !resourcekit.OptionalString(&empty).IsNull() || resourcekit.OptionalString(&set).ValueString() != "x" {
		t.Error("OptionalString: nil and empty must be null")
	}
	n := int64(5)
	if !resourcekit.OptionalInt64(nil).IsNull() || resourcekit.OptionalInt64(&n).ValueInt64() != 5 {
		t.Error("OptionalInt64")
	}
	if resourcekit.IntPtr(types.Int64Null()) != nil || resourcekit.IntPtr(types.Int64Unknown()) != nil {
		t.Error("IntPtr: null and unknown must be nil")
	}
	if p := resourcekit.IntPtr(types.Int64Value(7)); p == nil || *p != 7 {
		t.Error("IntPtr: known value")
	}
}

func TestWithTimeoutUsesTheConfiguredValueOrTheDefault(t *testing.T) {
	var diags diag.Diagnostics
	get := func(_ context.Context, def time.Duration) (time.Duration, diag.Diagnostics) { return def, nil }
	ctx, cancel, ok := resourcekit.WithTimeout(context.Background(), get, &diags)
	defer cancel()
	if !ok {
		t.Fatal("not ok")
	}
	deadline, has := ctx.Deadline()
	if !has || time.Until(deadline) > resourcekit.DefaultTimeout {
		t.Errorf("deadline = %v, has = %v", deadline, has)
	}

	bad := func(context.Context, time.Duration) (time.Duration, diag.Diagnostics) {
		var d diag.Diagnostics
		d.AddError("bad timeout", "nope")
		return 0, d
	}
	if _, cancel2, ok := resourcekit.WithTimeout(context.Background(), bad, &diags); ok || !diags.HasError() {
		t.Error("an invalid timeout must report not ok")
	} else {
		cancel2()
	}
}

func TestCheckID(t *testing.T) {
	var diags diag.Diagnostics
	if !resourcekit.CheckID(&diags, "network", "net_", "net_1") || diags.HasError() {
		t.Error("a right prefix must pass")
	}
	if resourcekit.CheckID(&diags, "network", "net_", "vm_1") || !diags.HasError() {
		t.Error("a wrong prefix must fail")
	}
}

func attr(field string) (path.Path, bool) {
	if field == "name" {
		return path.Root("name"), true
	}
	return path.Path{}, false
}

func TestAddAPIError(t *testing.T) {
	mapped := &client.APIError{Status: 422, Errors: []client.FieldError{{Field: "name", Code: "REQUIRED", Message: "is required"}}}
	unmapped := &client.APIError{Status: 422, Errors: []client.FieldError{{Field: "bogus", Code: "X", Message: "m"}}}

	var d diag.Diagnostics
	resourcekit.AddAPIError(&d, "Error", mapped, attr)
	if len(d) != 1 || len(d.Errors()) != 1 {
		t.Fatalf("diags = %v", d)
	}
	if _, isAttr := d[0].(interface{ Path() path.Path }); !isAttr {
		t.Error("a mapped field error must point at its attribute")
	}

	d = nil
	resourcekit.AddAPIError(&d, "Error", unmapped, attr)
	if len(d) != 1 || !strings.Contains(d[0].Detail(), "bogus") {
		t.Errorf("an unmapped field must fall back to one plain error with the field in it, got %v", d)
	}

	d = nil
	resourcekit.AddAPIError(&d, "Error", errors.New("plain"), nil)
	if len(d) != 1 || d[0].Detail() != "plain" {
		t.Errorf("diags = %v", d)
	}
}

func TestAddHintedAPIError(t *testing.T) {
	hints := map[string]string{"RULE_NUMBER_TAKEN": "pick another number"}

	var d diag.Diagnostics
	resourcekit.AddHintedAPIError(&d, "Error", &client.APIError{Status: 409, Code: "RULE_NUMBER_TAKEN", Detail: "taken"}, attr, hints)
	if !strings.Contains(d[0].Detail(), "pick another number") {
		t.Errorf("hint missing: %v", d)
	}

	d = nil
	resourcekit.AddHintedAPIError(&d, "Error", &client.APIError{Status: 409, Code: "OTHER", Detail: "other"}, attr, hints)
	if strings.Contains(d[0].Detail(), "pick another number") {
		t.Errorf("hint must only appear for its code: %v", d)
	}
}

func TestAddWaitError(t *testing.T) {
	var d diag.Diagnostics
	resourcekit.AddWaitError(&d, "Error", "network", "net_1", context.DeadlineExceeded)
	if !strings.Contains(d[0].Detail(), "net_1") || !strings.Contains(d[0].Detail(), "terraform refresh") {
		t.Errorf("a timeout must name the id and say to refresh: %v", d)
	}
	d = nil
	resourcekit.AddWaitError(&d, "Error", "network", "net_1", errors.New("failed"))
	if strings.Contains(d[0].Detail(), "terraform refresh") {
		t.Errorf("a plain failure needs no refresh advice: %v", d)
	}
}

func TestConfigureErrorNamesTheType(t *testing.T) {
	var d diag.Diagnostics
	resourcekit.ConfigureError(&d, 42)
	if !strings.Contains(d[0].Detail(), "int") {
		t.Errorf("diags = %v", d)
	}
}
