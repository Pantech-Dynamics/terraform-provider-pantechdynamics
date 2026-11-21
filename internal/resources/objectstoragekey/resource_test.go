package objectstoragekey

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

type fakeAPI struct {
	keys       []client.ObjectStorageKey
	createErr  error
	lastCreate client.CreateObjectStorageKeyRequest
	deletes    int
}

func (f *fakeAPI) CreateObjectStorageKey(_ context.Context, req client.CreateObjectStorageKeyRequest) (*client.ObjectStorageKeyWithSecret, error) {
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	buckets := req.Buckets
	if buckets == nil {
		buckets = []string{} // the API answers [] for "every bucket"
	}
	created := time.Date(2026, 11, 20, 9, 0, 0, 123000000, time.UTC)
	k := client.ObjectStorageKey{ID: "osk_1", Name: req.Name, Region: "af-abj", AccessKeyID: "AK1", ReadOnly: req.ReadOnly, Buckets: buckets, CreatedAt: &created}
	f.keys = append(f.keys, k)
	return &client.ObjectStorageKeyWithSecret{ObjectStorageKey: k, SecretAccessKey: "s3cr3t", Endpoint: "https://s3.af-abj.pantechdynamics.com"}, nil
}

func (f *fakeAPI) ListObjectStorageKeys(context.Context) ([]client.ObjectStorageKey, error) {
	return f.keys, nil
}

func (f *fakeAPI) DeleteObjectStorageKey(_ context.Context, id string) error {
	f.deletes++
	for i, k := range f.keys {
		if k.ID == id {
			f.keys = append(f.keys[:i], f.keys[i+1:]...)
			return nil
		}
	}
	return &client.APIError{Status: 404, Code: client.CodeObjectStorageKeyNotFound}
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

func create(t *testing.T, api *fakeAPI, buckets tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	plan := Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "name": Str("app"), "region": UnknownStr(), "read_only": tftypes.NewValue(tftypes.Bool, true),
		"buckets": buckets, "access_key_id": UnknownStr(), "secret_access_key": UnknownStr(), "endpoint": UnknownStr(), "created_at": UnknownStr(),
	})
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: plan}, resp)
	return resp
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func TestCreateKeepsTheSecret(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, strSet())
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.SecretAccessKey.ValueString() != "s3cr3t" || m.AccessKeyID.ValueString() != "AK1" || m.Endpoint.ValueString() == "" ||
		m.Region.ValueString() != "af-abj" || !m.Buckets.IsNull() || m.CreatedAt.ValueString() != "2026-11-20T09:00:00Z" {
		t.Fatalf("model = %+v", m)
	}
	if api.lastCreate.Buckets != nil || !api.lastCreate.ReadOnly || api.lastCreate.Region != "" {
		t.Fatalf("sent %+v", api.lastCreate)
	}
}

func TestCreateWithBuckets(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, strSet("logs", "uploads"))
	if m := getModel(t, resp.State); resp.Diagnostics.HasError() || len(m.Buckets.Elements()) != 2 || strings.Join(api.lastCreate.Buckets, ",") != "logs,uploads" {
		t.Fatalf("diagnostics = %v, sent %+v", resp.Diagnostics, api.lastCreate)
	}
}

func TestCreateRefusalIsExplained(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 403, Code: client.CodeObjectStorageNotEnabled}}
	resp := create(t, api, strSet())
	if !strings.Contains(ErrorText(resp.Diagnostics), "closed beta") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestReadKeepsTheSecretAndDropsARevokedKey(t *testing.T) {
	api := &fakeAPI{}
	st := create(t, api, strSet()).State
	resp := &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if m := getModel(t, resp.State); m.SecretAccessKey.ValueString() != "s3cr3t" || m.Endpoint.IsNull() {
		t.Fatalf("model = %+v", m)
	}
	api.keys = nil
	resp = &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestDeleteRevokesAndToleratesAGoneKey(t *testing.T) {
	api := &fakeAPI{}
	st := create(t, api, strSet()).State
	for range 2 {
		resp := &resource.DeleteResponse{State: st}
		(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
	}
	if api.deletes != 2 || len(api.keys) != 0 {
		t.Fatalf("deletes = %d, keys = %v", api.deletes, api.keys)
	}
}

func TestImportWarnsTheSecretIsNotImported(t *testing.T) {
	s := testSchema(t)
	resp := &resource.ImportStateResponse{State: EmptyState(s)}
	(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: "osk_1"}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	resp = &resource.ImportStateResponse{State: EmptyState(s)}
	(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: "bkt_1"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a wrong prefix must be refused")
	}
}

func TestEveryArgumentReplacesTheKey(t *testing.T) {
	s := testSchema(t)
	if a, ok := s.Attributes["name"].(schema.StringAttribute); !ok || len(a.PlanModifiers) == 0 {
		t.Error("name has no RequiresReplace")
	}
	if a, ok := s.Attributes["read_only"].(schema.BoolAttribute); !ok || len(a.PlanModifiers) == 0 {
		t.Error("read_only has no RequiresReplace")
	}
	if a, ok := s.Attributes["buckets"].(schema.SetAttribute); !ok || len(a.PlanModifiers) == 0 {
		t.Error("buckets has no RequiresReplace")
	}
	if a, ok := s.Attributes["secret_access_key"].(schema.StringAttribute); !ok || !a.Sensitive {
		t.Error("secret_access_key must be sensitive")
	}
}
