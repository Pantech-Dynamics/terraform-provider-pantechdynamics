package containerrepository

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory repositoryAPI. A deleted repository reads as
// deleting once, then 404, as the backend's 202 delete does.
type fakeAPI struct {
	repos map[string]client.Repository

	createErr, updateErr, deleteErr error
	creates, updates                int
	lastCreate                      client.CreateRepositoryRequest
	lastUpdate                      client.UpdateRepositoryRequest
}

func newFake() *fakeAPI { return &fakeAPI{repos: map[string]client.Repository{}} }

func notFound() error {
	return fmt.Errorf("getting repository: %w", &client.APIError{Status: 404, Code: client.CodeRepositoryNotFound})
}

func (f *fakeAPI) CreateRepository(_ context.Context, req client.CreateRepositoryRequest) (*client.Repository, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	r := client.Repository{ID: "crr_1", Namespace: "acme", Name: req.Name, FullName: "registry.pantechdynamics.com/acme/" + req.Name,
		Description: req.Description, Visibility: req.Visibility, ImmutableTags: *req.ImmutableTags, Retention: req.Retention, Status: client.RepositoryActive}
	if r.Retention == nil {
		r.Retention = &client.RepositoryRetention{} // the backend answers a policy of nulls
	}
	f.repos[r.ID] = r
	return &r, nil
}

func (f *fakeAPI) GetRepository(_ context.Context, id string) (*client.Repository, error) {
	r, ok := f.repos[id]
	if !ok {
		return nil, notFound()
	}
	if r.Status == client.RepositoryDeleting {
		delete(f.repos, id)
	}
	return &r, nil
}

func (f *fakeAPI) ListRepositories(context.Context, string) ([]client.Repository, error) {
	var out []client.Repository
	for _, r := range f.repos {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeAPI) UpdateRepository(_ context.Context, id string, req client.UpdateRepositoryRequest) (*client.Repository, error) {
	f.updates++
	f.lastUpdate = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	r := f.repos[id]
	if req.Description != nil {
		r.Description = *req.Description
	}
	if req.Visibility != nil {
		r.Visibility = *req.Visibility
	}
	if req.ImmutableTags != nil {
		r.ImmutableTags = *req.ImmutableTags
	}
	if req.Retention != nil {
		r.Retention = req.Retention
	}
	f.repos[id] = r
	return &r, nil
}

func (f *fakeAPI) DeleteRepository(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	r, ok := f.repos[id]
	if !ok {
		return notFound()
	}
	r.Status = client.RepositoryDeleting
	f.repos[id] = r
	return nil
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	for range 3 {
		ok, err := done(ctx)
		if err != nil || ok {
			return err
		}
	}
	return context.DeadlineExceeded
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func retentionType(s schema.Schema) tftypes.Object {
	typ, ok := s.Attributes["retention"].GetType().TerraformType(Ctx).(tftypes.Object)
	if !ok {
		panic("retention is not an object")
	}
	return typ
}

// retention builds the block; nil omits a rule, and a nil map is no block.
func retention(s schema.Schema, keepLast *int64, pattern *string) tftypes.Value {
	typ := retentionType(s)
	if keepLast == nil && pattern == nil {
		return tftypes.NewValue(typ, nil)
	}
	vals := map[string]tftypes.Value{
		"keep_last": tftypes.NewValue(tftypes.Number, nil), "untagged_after_days": tftypes.NewValue(tftypes.Number, nil), "keep_pattern": tftypes.NewValue(tftypes.String, nil),
	}
	if keepLast != nil {
		vals["keep_last"] = Num(*keepLast)
	}
	if pattern != nil {
		vals["keep_pattern"] = Str(*pattern)
	}
	return tftypes.NewValue(typ, vals)
}

func i64(n int64) *int64   { return &n }
func str(s string) *string { return &s }

func planFor(s schema.Schema, id tftypes.Value, visibility string, ret tftypes.Value) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": id, "name": Str("api/server"), "description": Str(""), "visibility": Str(visibility), "immutable_tags": tftypes.NewValue(tftypes.Bool, false),
		"retention": ret, "namespace": UnknownStr(), "full_name": UnknownStr(), "tag_count": Unknown(tftypes.Number), "storage_bytes": Unknown(tftypes.Number),
		"last_pushed_at": UnknownStr(), "status": UnknownStr(), "created_at": UnknownStr(),
	})
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI, ret tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, UnknownStr(), "private", ret)}, resp)
	return resp
}

// The backend answers a policy of nulls for "no retention"; state keeps the
// block null so the configuration without one stays stable.
func TestCreateWithoutRetentionKeepsTheBlockNull(t *testing.T) {
	api := newFake()
	resp := create(t, api, retention(testSchema(t), nil, nil))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Retention != nil || m.ID.ValueString() != "crr_1" || m.FullName.ValueString() != "registry.pantechdynamics.com/acme/api/server" || api.lastCreate.Retention != nil {
		t.Fatalf("model = %+v, sent %+v", m, api.lastCreate)
	}
}

func TestCreateWithRetention(t *testing.T) {
	api := newFake()
	resp := create(t, api, retention(testSchema(t), i64(50), str("v*")))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Retention == nil || m.Retention.KeepLast.ValueInt64() != 50 || m.Retention.KeepPattern.ValueString() != "v*" || !m.Retention.UntaggedAfterDays.IsNull() {
		t.Fatalf("retention = %+v", m.Retention)
	}
	if r := api.lastCreate.Retention; r == nil || *r.KeepLast != 50 || r.UntaggedAfterDays != nil {
		t.Fatalf("sent %+v", r)
	}
}

func TestCreateRefusalsAreExplained(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&client.APIError{Status: 409, Code: client.CodeRepositoryExists}, "terraform import"},
		{&client.APIError{Status: 404, Code: client.CodeRegistryNamespaceNotSet}, "Claim a namespace first"},
		{&client.APIError{Status: 403, Code: "REPOSITORY_LIMIT_REACHED"}, "(100)"},
	}
	for _, tt := range tests {
		api := newFake()
		api.createErr = tt.err
		resp := create(t, api, retention(testSchema(t), nil, nil))
		if !strings.Contains(ErrorText(resp.Diagnostics), tt.want) || !IsRemoved(resp.State) {
			t.Errorf("diagnostics = %q, want %q", ErrorText(resp.Diagnostics), tt.want)
		}
	}
}

func stateOf(t *testing.T, api *fakeAPI, ret tftypes.Value) tfsdk.State {
	t.Helper()
	resp := create(t, api, ret)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	return resp.State
}

// Removing the retention block sends a policy of nulls, which clears it.
func TestUpdateRemovingRetentionClearsIt(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := stateOf(t, api, retention(s, i64(50), nil))
	resp := &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: planFor(s, Str("crr_1"), "public", retention(s, nil, nil)), State: st}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	u := api.lastUpdate
	if u.Visibility == nil || *u.Visibility != "public" || u.Retention == nil || !u.Retention.IsEmpty() || u.Description != nil || u.ImmutableTags != nil {
		t.Fatalf("sent %+v", u)
	}
	if m := getModel(t, resp.State); m.Retention != nil || m.Visibility.ValueString() != "public" {
		t.Fatalf("model = %+v", m)
	}
}

func TestUpdateWithNothingChangedSendsNothing(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := stateOf(t, api, retention(s, nil, nil))
	resp := &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: planFor(s, Str("crr_1"), "private", retention(s, nil, nil)), State: st}, resp)
	if resp.Diagnostics.HasError() || api.updates != 0 {
		t.Fatalf("diagnostics = %v, updates = %d", resp.Diagnostics, api.updates)
	}
}

func TestReadRemovesARepositoryGoneOrDeleting(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := stateOf(t, api, retention(s, nil, nil))
	r := api.repos["crr_1"]
	r.Status = client.RepositoryDeleting
	api.repos["crr_1"] = r
	for range 2 { // deleting, then 404
		resp := &resource.ReadResponse{State: st}
		(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
		if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
			t.Fatalf("diagnostics = %v", resp.Diagnostics)
		}
	}
}

func TestDeleteWaitsForTheRepositoryToGo(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := stateOf(t, api, retention(s, nil, nil))
	resp := &resource.DeleteResponse{State: st}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || len(api.repos) != 0 {
		t.Fatalf("diagnostics = %v, repos = %v", resp.Diagnostics, api.repos)
	}
	// Already gone: success.
	resp = &resource.DeleteResponse{State: st}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	api = newFake()
	api.repos["crr_1"] = client.Repository{ID: "crr_1"}
	api.deleteErr = &client.APIError{Status: 409, Code: "REPOSITORY_SUSPENDED"}
	resp = &resource.DeleteResponse{State: st}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "suspended") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestImportByIDOrName(t *testing.T) {
	api := newFake()
	api.repos["crr_7"] = client.Repository{ID: "crr_7", Name: "api/server"}
	tests := []struct{ id, wantID, wantErr string }{
		{"crr_7", "crr_7", ""},
		{"api/server", "crr_7", ""},
		{"web", "", "no repository named"},
		{"Not Valid", "", "Invalid import id"},
	}
	for _, tt := range tests {
		s := testSchema(t)
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{api: api}).ImportState(Ctx, resource.ImportStateRequest{ID: tt.id}, resp)
		if got := ErrorText(resp.Diagnostics); (tt.wantErr == "") != (got == "") || !strings.Contains(got, tt.wantErr) {
			t.Errorf("%s: diagnostics = %q", tt.id, got)
			continue
		}
		if tt.wantErr == "" && getModel(t, resp.State).ID.ValueString() != tt.wantID {
			t.Errorf("%s: id = %s", tt.id, getModel(t, resp.State).ID.ValueString())
		}
	}
}

func TestValidateConfigRefusesAnEmptyRetentionBlock(t *testing.T) {
	s := testSchema(t)
	typ := retentionType(s)
	empty := tftypes.NewValue(typ, map[string]tftypes.Value{
		"keep_last": tftypes.NewValue(tftypes.Number, nil), "untagged_after_days": tftypes.NewValue(tftypes.Number, nil), "keep_pattern": tftypes.NewValue(tftypes.String, nil),
	})
	for _, tt := range []struct {
		ret     tftypes.Value
		wantErr bool
	}{{empty, true}, {retention(s, i64(5), nil), false}, {retention(s, nil, nil), false}} {
		cfg := tfsdk.Config{Schema: s, Raw: Object(s, map[string]tftypes.Value{"name": Str("api"), "retention": tt.ret})}
		resp := &resource.ValidateConfigResponse{}
		(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
		if resp.Diagnostics.HasError() != tt.wantErr {
			t.Errorf("diagnostics = %v, want error %v", resp.Diagnostics, tt.wantErr)
		}
	}
}
