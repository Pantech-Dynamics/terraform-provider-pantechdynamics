package registrytoken

import (
	"context"
	"strings"
	"testing"
	"time"

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

type fakeAPI struct {
	tokens     []client.RegistryToken
	createErr  error
	noSecret   bool
	lastCreate client.CreateRegistryTokenRequest
	deletes    int
}

func (f *fakeAPI) CreateRegistryToken(_ context.Context, req client.CreateRegistryTokenRequest) (*client.RegistryTokenWithSecret, error) {
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	repos := req.Repositories
	if repos == nil {
		repos = []string{} // the API answers [] for "every repository"
	}
	created := time.Date(2026, 11, 10, 9, 0, 0, 123000000, time.UTC)
	t := client.RegistryToken{ID: "prt_1", Name: req.Name, Permission: req.Permission, Repositories: repos, ExpiresAt: req.ExpiresAt, CreatedAt: &created}
	f.tokens = append(f.tokens, t)
	out := &client.RegistryTokenWithSecret{RegistryToken: t}
	if !f.noSecret {
		s := "prt_1_secret"
		out.Secret = &s
	}
	return out, nil
}

func (f *fakeAPI) ListRegistryTokens(context.Context) ([]client.RegistryToken, error) {
	return f.tokens, nil
}

func (f *fakeAPI) DeleteRegistryToken(_ context.Context, id string) error {
	f.deletes++
	for i, t := range f.tokens {
		if t.ID == id {
			f.tokens = append(f.tokens[:i], f.tokens[i+1:]...)
			return nil
		}
	}
	return &client.APIError{Status: 404, Code: client.CodeRegistryTokenNotFound}
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func strSet(vals ...string) tftypes.Value {
	typ := tftypes.Set{ElementType: tftypes.String}
	if vals == nil {
		return tftypes.NewValue(typ, nil)
	}
	elems := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, Str(v))
	}
	return tftypes.NewValue(typ, elems)
}

func planFor(s schema.Schema, repos, expires tftypes.Value) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "name": Str("ci"), "permission": Str("push"), "repositories": repos, "expires_at": expires,
		"secret": UnknownStr(), "last_used_at": UnknownStr(), "created_at": UnknownStr(),
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

func create(t *testing.T, api *fakeAPI, repos, expires tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, repos, expires)}, resp)
	return resp
}

// The secret is kept in state; an expiry written with an offset and "every
// repository" (no list, answered as []) stay as configured.
func TestCreateKeepsTheSecretAndTheConfiguredSpelling(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, strSet(), Str("2027-11-10T01:00:00+01:00"))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Secret.ValueString() != "prt_1_secret" || !m.Repositories.IsNull() || m.ExpiresAt.ValueString() != "2027-11-10T01:00:00+01:00" || m.CreatedAt.ValueString() != "2026-11-10T09:00:00Z" {
		t.Fatalf("model = %+v", m)
	}
	if api.lastCreate.Repositories != nil || !api.lastCreate.ExpiresAt.Equal(time.Date(2027, 11, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("sent %+v", api.lastCreate)
	}
}

func TestCreateWithRepositories(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, strSet("web", "api/server"), tftypes.NewValue(tftypes.String, nil))
	m := getModel(t, resp.State)
	if resp.Diagnostics.HasError() || len(m.Repositories.Elements()) != 2 || !m.ExpiresAt.IsNull() || strings.Join(api.lastCreate.Repositories, ",") != "api/server,web" {
		t.Fatalf("diagnostics = %v, model = %+v, sent %+v", resp.Diagnostics, m, api.lastCreate)
	}
}

func TestCreateWithoutSecretWarns(t *testing.T) {
	api := &fakeAPI{noSecret: true}
	resp := create(t, api, strSet(), tftypes.NewValue(tftypes.String, nil))
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 || !getModel(t, resp.State).Secret.IsNull() {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestCreateRefusalsAreExplained(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 403, Code: "REGISTRY_TOKEN_LIMIT_REACHED"}}
	resp := create(t, api, strSet(), tftypes.NewValue(tftypes.String, nil))
	if !strings.Contains(ErrorText(resp.Diagnostics), "(50)") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestReadKeepsTheSecretAndDropsARevokedToken(t *testing.T) {
	api := &fakeAPI{}
	st := create(t, api, strSet(), tftypes.NewValue(tftypes.String, nil)).State
	used := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	api.tokens[0].LastUsedAt = &used
	resp := &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if m := getModel(t, resp.State); m.Secret.ValueString() != "prt_1_secret" || m.LastUsedAt.ValueString() != "2026-11-11T08:00:00Z" {
		t.Fatalf("model = %+v", m)
	}
	api.tokens[0].RevokedAt = &used
	resp = &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if !IsRemoved(resp.State) {
		t.Fatal("a revoked token must leave state")
	}
	api.tokens = nil
	resp = &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestDeleteRevokesAndToleratesAGoneToken(t *testing.T) {
	api := &fakeAPI{}
	st := create(t, api, strSet(), tftypes.NewValue(tftypes.String, nil)).State
	for range 2 {
		resp := &resource.DeleteResponse{State: st}
		(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
	}
	if api.deletes != 2 || len(api.tokens) != 0 {
		t.Fatalf("deletes = %d, tokens = %v", api.deletes, api.tokens)
	}
}

func TestImportWarnsTheSecretIsNotImported(t *testing.T) {
	s := testSchema(t)
	resp := &resource.ImportStateResponse{State: EmptyState(s)}
	(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: "prt_1"}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	resp = &resource.ImportStateResponse{State: EmptyState(s)}
	(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: "crr_1"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a wrong prefix must be refused")
	}
}

func TestEveryArgumentReplacesTheToken(t *testing.T) {
	s := testSchema(t)
	for _, name := range []string{"name", "permission", "expires_at"} {
		if a, ok := s.Attributes[name].(schema.StringAttribute); !ok || len(a.PlanModifiers) == 0 {
			t.Errorf("%s has no RequiresReplace", name)
		}
	}
	if a, ok := s.Attributes["repositories"].(schema.SetAttribute); !ok || len(a.PlanModifiers) == 0 {
		t.Error("repositories has no RequiresReplace")
	}
	if a, ok := s.Attributes["secret"].(schema.StringAttribute); !ok || !a.Sensitive {
		t.Error("secret must be sensitive")
	}
}

func TestValidators(t *testing.T) {
	for v, ok := range map[string]bool{"2027-11-10T00:00:00Z": true, "2027-11-10": false, "tomorrow": false} {
		var r validator.StringResponse
		rfc3339{}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("expires_at"), ConfigValue: types.StringValue(v)}, &r)
		if r.Diagnostics.HasError() == ok {
			t.Errorf("%q: error = %v", v, r.Diagnostics.HasError())
		}
	}
	bad, _ := types.SetValueFrom(Ctx, types.StringType, []string{"api/server", "Bad Name"})
	var r validator.SetResponse
	repositoryNames{}.ValidateSet(Ctx, validator.SetRequest{Path: path.Root("repositories"), ConfigValue: bad}, &r)
	if !r.Diagnostics.HasError() {
		t.Error("an invalid repository name must be refused")
	}
}
