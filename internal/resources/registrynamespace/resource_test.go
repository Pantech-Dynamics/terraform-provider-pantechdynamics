package registrynamespace

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory namespaceAPI. With repositories > 0 a rename is
// refused, as the backend refuses it.
type fakeAPI struct {
	ns           *client.RegistryNamespace
	repositories int64
	setErr       error
	sets         int
	lastSet      client.SetRegistryNamespaceRequest
}

func notSet() error {
	return &client.APIError{Status: 404, Code: client.CodeRegistryNamespaceNotSet}
}

func (f *fakeAPI) GetRegistryNamespace(context.Context) (*client.RegistryNamespace, error) {
	if f.ns == nil {
		return nil, notSet()
	}
	ns := *f.ns
	return &ns, nil
}

func (f *fakeAPI) SetRegistryNamespace(_ context.Context, req client.SetRegistryNamespaceRequest) (*client.RegistryNamespace, error) {
	f.sets++
	f.lastSet = req
	if f.setErr != nil {
		return nil, f.setErr
	}
	if f.ns != nil && f.ns.Name != req.Name && f.repositories > 0 {
		return nil, &client.APIError{Status: 409, Code: client.CodeRegistryNamespaceLocked}
	}
	f.ns = &client.RegistryNamespace{Name: req.Name, RegistryHost: "registry.pantechdynamics.com", CreateOnPush: *req.CreateOnPush,
		RepositoryCount: f.repositories, StorageLimitBytes: 53687091200, RepositoryLimit: 100}
	return f.GetRegistryNamespace(context.Background())
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func planFor(s schema.Schema, name string, createOnPush bool) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "name": Str(name), "create_on_push": tftypes.NewValue(tftypes.Bool, createOnPush), "registry_host": UnknownStr(),
		"repository_count": Unknown(tftypes.Number), "storage_bytes": Unknown(tftypes.Number), "storage_limit_bytes": Unknown(tftypes.Number),
		"repository_limit": Unknown(tftypes.Number), "created_at": UnknownStr(),
	})
}

func stateFor(s schema.Schema) tfsdk.State {
	return State(s, map[string]tftypes.Value{"id": Str("acme"), "name": Str("acme"), "create_on_push": tftypes.NewValue(tftypes.Bool, true)})
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func TestCreateClaimsTheNamespace(t *testing.T) {
	api := &fakeAPI{}
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, "acme", false)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "acme" || m.CreateOnPush.ValueBool() || m.RegistryHost.ValueString() != "registry.pantechdynamics.com" || m.RepositoryLimit.ValueInt64() != 100 {
		t.Fatalf("model = %+v", m)
	}
	if *api.lastSet.CreateOnPush {
		t.Fatal("create_on_push = false must be sent")
	}
}

// Renaming a namespace that holds repositories is refused; the error names the
// namespace the organization holds, on the name attribute.
func TestRenameOfALockedNamespaceIsExplained(t *testing.T) {
	api := &fakeAPI{ns: &client.RegistryNamespace{Name: "acme", RepositoryCount: 3}, repositories: 3}
	s := testSchema(t)
	resp := &resource.UpdateResponse{State: stateFor(s)}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: planFor(s, "acme-two", true), State: stateFor(s)}, resp)
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || errs[0].Summary() != "Registry namespace cannot be renamed" || !strings.Contains(errs[0].Detail(), `set name = "acme"`) || !strings.Contains(errs[0].Detail(), "3 repositories") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestCreateRefusalsAreExplained(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&client.APIError{Status: 409, Code: client.CodeRegistryNamespaceTaken}, "Another organization"},
		{&client.APIError{Status: 503, Code: client.CodeRegistryUnavailable}, "not available"},
		{&client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "name", Code: "INVALID_REGISTRY_NAMESPACE", Message: "is reserved"}}}, "is reserved"},
	}
	for _, tt := range tests {
		api := &fakeAPI{setErr: tt.err}
		s := testSchema(t)
		resp := &resource.CreateResponse{State: EmptyState(s)}
		(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, "acme", true)}, resp)
		if !strings.Contains(ErrorText(resp.Diagnostics), tt.want) || !IsRemoved(resp.State) {
			t.Errorf("diagnostics = %q, want %q", ErrorText(resp.Diagnostics), tt.want)
		}
	}
}

func TestReadRefreshesAndRemovesAnUnsetNamespace(t *testing.T) {
	api := &fakeAPI{ns: &client.RegistryNamespace{Name: "acme", StorageBytes: 4096, RepositoryCount: 2, CreateOnPush: true}}
	s := testSchema(t)
	resp := &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if m := getModel(t, resp.State); m.StorageBytes.ValueInt64() != 4096 || m.RepositoryCount.ValueInt64() != 2 {
		t.Fatalf("model = %+v", m)
	}
	api.ns = nil
	resp = &resource.ReadResponse{State: stateFor(s)}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestDeleteOnlyWarns(t *testing.T) {
	api := &fakeAPI{ns: &client.RegistryNamespace{Name: "acme"}}
	s := testSchema(t)
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 || api.sets != 0 || api.ns == nil {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestModifyPlanWarnsOnRenameAndDestroy(t *testing.T) {
	api := &fakeAPI{}
	s := testSchema(t)
	resp := &resource.ModifyPlanResponse{Plan: planFor(s, "acme-two", true)}
	(&Resource{api: api}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: planFor(s, "acme-two", true), State: stateFor(s)}, resp)
	if resp.Diagnostics.WarningsCount() != 1 || resp.Diagnostics.Warnings()[0].Summary() != "Renaming the registry namespace" {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	null := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(Ctx), nil)}
	resp = &resource.ModifyPlanResponse{Plan: null}
	(&Resource{api: api}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: null, State: stateFor(s)}, resp)
	if resp.Diagnostics.WarningsCount() != 1 || resp.Diagnostics.Warnings()[0].Summary() != "Destroy does not delete the namespace" {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	resp = &resource.ModifyPlanResponse{Plan: planFor(s, "acme", true)}
	(&Resource{api: api}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: planFor(s, "acme", true), State: stateFor(s)}, resp)
	if resp.Diagnostics.WarningsCount() != 0 {
		t.Fatalf("an unchanged name must not warn: %v", resp.Diagnostics)
	}
}

func TestImportChecksTheName(t *testing.T) {
	tests := []struct {
		ns      *client.RegistryNamespace
		id      string
		wantErr string
	}{
		{&client.RegistryNamespace{Name: "acme"}, "acme", ""},
		{&client.RegistryNamespace{Name: "acme"}, "acne", `namespace is "acme"`},
		{nil, "acme", "not claimed"},
	}
	for _, tt := range tests {
		s := testSchema(t)
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{api: &fakeAPI{ns: tt.ns}}).ImportState(Ctx, resource.ImportStateRequest{ID: tt.id}, resp)
		if got := ErrorText(resp.Diagnostics); (tt.wantErr == "") != (got == "") || !strings.Contains(got, tt.wantErr) {
			t.Errorf("%s: diagnostics = %q", tt.id, got)
		}
	}
}

func TestNamespaceValidator(t *testing.T) {
	for name, ok := range map[string]bool{
		"acme": true, "acme-corp": true, "a1b": true, "ab": false, "1acme": false, "Acme": false,
		"acme--corp": false, "acme_corp": false, "library": false, strings.Repeat("a", 31): false,
	} {
		var r validator.StringResponse
		namespaceName{}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("name"), ConfigValue: types.StringValue(name)}, &r)
		if r.Diagnostics.HasError() == ok {
			t.Errorf("%q: error = %v", name, r.Diagnostics.HasError())
		}
	}
}
