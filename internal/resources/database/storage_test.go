package database

import (
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// sized is a stored database with a 20 GB data disk.
func sized(observed string) *fakeAPI {
	api := seeded(observed)
	api.dbs[0].DataVolumeSizeGB = 20
	return api
}

func storedWith(s schema.Schema, desired string, gb int64) tfsdk.State {
	st := stateFor(s, desired, Num(1))
	st.Raw = withValue(st.Raw, "storage_gb", Num(gb))
	st.Raw = withValue(st.Raw, "data_volume_size_gb", Num(gb))
	return st
}

func TestCreateSendsTheChosenStorage(t *testing.T) {
	api := &fakeAPI{}
	a := args("running", cidrSet())
	a["storage_gb"] = Num(60)
	resp := create(t, api, a)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.lastCreate.StorageGB == nil || *api.lastCreate.StorageGB != 60 {
		t.Fatalf("storage_gb sent = %v", api.lastCreate.StorageGB)
	}
	if m := getModel(t, resp.State); m.StorageGB.ValueInt64() != 60 || m.DataVolumeSizeGB.ValueInt64() != 60 {
		t.Fatalf("storage_gb = %v, data_volume_size_gb = %v", m.StorageGB, m.DataVolumeSizeGB)
	}
}

func TestCreateWithoutStorageUsesThePlanSize(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, args("running", cidrSet()))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.lastCreate.StorageGB != nil {
		t.Fatalf("storage_gb sent = %d, want omitted", *api.lastCreate.StorageGB)
	}
	if m := getModel(t, resp.State); m.StorageGB.ValueInt64() != 20 {
		t.Fatalf("storage_gb = %v, want the plan's 20", m.StorageGB)
	}
}

func TestUpdateGrowsStorageInPlace(t *testing.T) {
	t.Run("a running database is resized and nothing else is touched", func(t *testing.T) {
		api := sized("running")
		s := testSchema(t)
		a := args("running", cidrSet("10.0.0.0/16"))
		a["storage_gb"] = Num(40)
		resp := update(t, api, storedWith(s, "running", 20), a, Num(1), testPassword)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if !slices.Equal(api.resizes, []int64{40}) || !slices.Equal(api.actionOrder, []string{"resize"}) || api.creates != 0 || api.deletes != 0 {
			t.Fatalf("resizes = %v, actions = %v", api.resizes, api.actionOrder)
		}
		if m := getModel(t, resp.State); m.StorageGB.ValueInt64() != 40 || m.DataVolumeSizeGB.ValueInt64() != 40 {
			t.Fatalf("model = %+v", m)
		}
	})

	t.Run("a stopped database is started for the resize and stopped again", func(t *testing.T) {
		api := sized("stopped")
		s := testSchema(t)
		a := args("stopped", cidrSet("10.0.0.0/16"))
		a["storage_gb"] = Num(40)
		resp := update(t, api, storedWith(s, "stopped", 20), a, Num(1), testPassword)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if !slices.Equal(api.actionOrder, []string{"start", "resize", "stop"}) {
			t.Fatalf("actions = %v", api.actionOrder)
		}
	})

	t.Run("a resize in flight is waited for until the size is in place", func(t *testing.T) {
		api := sized("running")
		api.resizeLags = true
		s := testSchema(t)
		a := args("running", cidrSet("10.0.0.0/16"))
		a["storage_gb"] = Num(40)
		resp := update(t, api, storedWith(s, "running", 20), a, Num(1), testPassword)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if m := getModel(t, resp.State); m.DataVolumeSizeGB.ValueInt64() != 40 {
			t.Fatalf("data_volume_size_gb = %v", m.DataVolumeSizeGB)
		}
	})

	t.Run("an interrupted resize still in flight is not sent again", func(t *testing.T) {
		api := sized("running")
		api.dbs[0].PendingDataVolumeSizeGB = ptr(int64(40))
		s := testSchema(t)
		a := args("running", cidrSet("10.0.0.0/16"))
		a["storage_gb"] = Num(40)
		resp := update(t, api, storedWith(s, "running", 20), a, Num(1), testPassword)
		if resp.Diagnostics.HasError() || len(api.resizes) != 0 {
			t.Fatalf("diags = %v, resizes = %v", resp.Diagnostics, api.resizes)
		}
	})

	t.Run("the API's refusal is shown on storage_gb with its message", func(t *testing.T) {
		api := sized("running")
		api.resizeErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Detail: "The request is invalid.",
			Errors: []client.FieldError{{Field: "storage_gb", Code: "INVALID_DATABASE_STORAGE", Message: "must be a multiple of 10 GB above the plan's size"}}}
		s := testSchema(t)
		a := args("running", cidrSet("10.0.0.0/16"))
		a["storage_gb"] = Num(45)
		resp := update(t, api, storedWith(s, "running", 20), a, Num(1), testPassword)
		if !attachedTo(resp.Diagnostics, path.Root("storage_gb")) || !strings.Contains(ErrorText(resp.Diagnostics), "multiple of 10 GB") {
			t.Fatalf("diags = %s", ErrorText(resp.Diagnostics))
		}
		if m := getModel(t, resp.State); m.StorageGB.ValueInt64() != 20 {
			t.Fatalf("storage_gb in state = %v: a refused resize must not be recorded", m.StorageGB)
		}
	})

	t.Run("a resize already running elsewhere gets a hint", func(t *testing.T) {
		api := sized("running")
		api.resizeErr = &client.APIError{Status: 409, Code: "DATABASE_STORAGE_RESIZE_IN_PROGRESS", Detail: "A storage resize is already in progress."}
		s := testSchema(t)
		a := args("running", cidrSet("10.0.0.0/16"))
		a["storage_gb"] = Num(40)
		resp := update(t, api, storedWith(s, "running", 20), a, Num(1), testPassword)
		text := ErrorText(resp.Diagnostics)
		if !strings.Contains(text, "A storage resize is already in progress.") || !strings.Contains(text, "Wait for it to finish") {
			t.Fatalf("diags = %s", text)
		}
	})
}

func modifyPlan(t *testing.T, state tfsdk.State, configured tftypes.Value, replace bool) *resource.ModifyPlanResponse {
	t.Helper()
	s := testSchema(t)
	a := args("running", cidrSet("10.0.0.0/16"))
	a["storage_gb"] = configured
	cfg := configFor(s, a, testPassword)
	plan := planFor(s, a)
	plan.Raw = withValue(plan.Raw, "id", Str("db_1"))
	if configured.IsNull() {
		plan.Raw = withValue(plan.Raw, "storage_gb", Unknown(tftypes.Number))
	}
	resp := &resource.ModifyPlanResponse{Plan: plan}
	if replace {
		resp.RequiresReplace = path.Paths{path.Root("name")}
	}
	(&Resource{}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Config: cfg, Plan: plan, State: state}, resp)
	return resp
}

func plannedInt(t *testing.T, p tfsdk.Plan, name string) types.Int64 {
	t.Helper()
	var v types.Int64
	if d := p.GetAttribute(Ctx, path.Root(name), &v); d.HasError() {
		t.Fatal(d)
	}
	return v
}

func TestStorageOnlyGrowsAtPlanTime(t *testing.T) {
	s := testSchema(t)
	stored := storedWith(s, "running", 40)

	t.Run("a smaller size is an error on storage_gb, not a replacement", func(t *testing.T) {
		resp := modifyPlan(t, stored, Num(20), false)
		if !attachedTo(resp.Diagnostics, path.Root("storage_gb")) || !strings.Contains(ErrorText(resp.Diagnostics), "can only grow") {
			t.Fatalf("diags = %s", ErrorText(resp.Diagnostics))
		}
		if len(resp.RequiresReplace) != 0 {
			t.Fatalf("requires replace = %v", resp.RequiresReplace)
		}
	})

	t.Run("a bigger size plans an in-place change with the new size unknown", func(t *testing.T) {
		resp := modifyPlan(t, stored, Num(60), false)
		if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 0 {
			t.Fatalf("diags = %v, replace = %v", resp.Diagnostics, resp.RequiresReplace)
		}
		if got := plannedInt(t, resp.Plan, "storage_gb"); got.ValueInt64() != 60 {
			t.Fatalf("storage_gb = %v", got)
		}
		if got := plannedInt(t, resp.Plan, "data_volume_size_gb"); !got.IsUnknown() {
			t.Fatalf("data_volume_size_gb = %v, want unknown", got)
		}
	})

	t.Run("unset keeps the current size and data_volume_size_gb", func(t *testing.T) {
		resp := modifyPlan(t, stored, tftypes.NewValue(tftypes.Number, nil), false)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		if got := plannedInt(t, resp.Plan, "storage_gb"); got.ValueInt64() != 40 {
			t.Fatalf("storage_gb = %v", got)
		}
		if got := plannedInt(t, resp.Plan, "data_volume_size_gb"); got.ValueInt64() != 40 {
			t.Fatalf("data_volume_size_gb = %v", got)
		}
	})

	t.Run("a database being replaced anyway may start smaller", func(t *testing.T) {
		if resp := modifyPlan(t, stored, Num(20), true); resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
	})
}

func TestReadReportsAResizeInFlightAsItsTarget(t *testing.T) {
	api := sized("running")
	api.dbs[0].PendingDataVolumeSizeGB = ptr(int64(40))
	s := testSchema(t)
	resp := &resource.ReadResponse{State: storedWith(s, "running", 20)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: storedWith(s, "running", 20)}, resp)
	if m := getModel(t, resp.State); m.StorageGB.ValueInt64() != 40 || m.DataVolumeSizeGB.ValueInt64() != 20 {
		t.Fatalf("storage_gb = %v, data_volume_size_gb = %v", m.StorageGB, m.DataVolumeSizeGB)
	}
}

// withValue returns the object value with one attribute replaced.
func withValue(raw tftypes.Value, name string, v tftypes.Value) tftypes.Value {
	var attrs map[string]tftypes.Value
	if err := raw.As(&attrs); err != nil {
		panic(err)
	}
	attrs[name] = v
	return tftypes.NewValue(raw.Type(), attrs)
}

// attachedTo reports whether an error diagnostic points at p.
func attachedTo(d diag.Diagnostics, p path.Path) bool {
	for _, e := range d.Errors() {
		if withPath, ok := e.(diag.DiagnosticWithPath); ok && withPath.Path().Equal(p) {
			return true
		}
	}
	return false
}
