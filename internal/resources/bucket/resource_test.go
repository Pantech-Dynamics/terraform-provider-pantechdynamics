package bucket

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

// fakeAPI keeps buckets in memory. A new bucket reads as provisioning once,
// then active, the way the store settles it.
type fakeAPI struct {
	buckets    map[string]*client.Bucket
	reads      map[string]int
	createErr  error
	deleteErr  error
	lastCreate client.CreateBucketRequest
	lastUpdate *client.UpdateBucketRequest
	lastForce  *bool
}

func newFake() *fakeAPI {
	return &fakeAPI{buckets: map[string]*client.Bucket{}, reads: map[string]int{}}
}

func (f *fakeAPI) CreateBucket(_ context.Context, req client.CreateBucketRequest) (*client.Bucket, error) {
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	region := req.Region
	if region == "" {
		region = "af-abj"
	}
	created := time.Date(2026, 11, 20, 9, 0, 0, 123000000, time.UTC)
	op := "op_1"
	b := &client.Bucket{ID: "bkt_1", Name: req.Name, Region: region, Endpoint: "https://s3." + region + ".pantechdynamics.com",
		QuotaBytes: req.QuotaBytes, Versioning: req.Versioning, ObjectLock: req.ObjectLock, PublicPrefixes: append([]string{}, req.PublicPrefixes...),
		State: "provisioning", CreatedAt: &created}
	f.buckets[b.ID] = b
	out := *b
	out.OperationID = &op
	return &out, nil
}

func (f *fakeAPI) GetBucket(_ context.Context, id string) (*client.Bucket, error) {
	b, ok := f.buckets[id]
	if !ok {
		return nil, &client.APIError{Status: 404, Code: client.CodeBucketNotFound}
	}
	f.reads[id]++
	if b.State == "provisioning" || b.State == "updating" {
		b.State = client.BucketActive // settles on the first look
		cp := *b
		cp.State = "provisioning"
		return &cp, nil
	}
	cp := *b
	return &cp, nil
}

func (f *fakeAPI) ListBuckets(context.Context) ([]client.Bucket, error) {
	var out []client.Bucket
	for _, b := range f.buckets {
		out = append(out, *b)
	}
	return out, nil
}

func (f *fakeAPI) UpdateBucket(_ context.Context, id string, req client.UpdateBucketRequest) (*client.Bucket, error) {
	f.lastUpdate = &req
	b := f.buckets[id]
	if req.QuotaBytes != nil {
		b.QuotaBytes = *req.QuotaBytes
	}
	if req.Versioning != nil {
		b.Versioning = *req.Versioning
	}
	if req.PublicPrefixes != nil {
		b.PublicPrefixes = *req.PublicPrefixes
	}
	b.State = "updating"
	cp := *b
	return &cp, nil
}

func (f *fakeAPI) DeleteBucket(_ context.Context, id string, force bool) error {
	f.lastForce = &force
	if f.deleteErr != nil {
		return f.deleteErr
	}
	b, ok := f.buckets[id]
	if !ok {
		return &client.APIError{Status: 404, Code: client.CodeBucketNotFound}
	}
	b.State = client.BucketDeleting
	return nil
}

func (f *fakeAPI) GetOperation(context.Context, string) (*client.Operation, error) {
	return &client.Operation{Status: client.OperationFailed}, nil
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	for range 5 {
		ok, err := done(ctx)
		if err != nil || ok {
			return err
		}
	}
	return Wait(ctx, done, true)
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

func values(quota int64, versioning bool, prefixes tftypes.Value) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"id": UnknownStr(), "name": Str("uploads"), "region": UnknownStr(), "quota_bytes": Num(quota),
		"versioning": tftypes.NewValue(tftypes.Bool, versioning), "object_lock": tftypes.NewValue(tftypes.Bool, false),
		"public_prefixes": prefixes, "force_destroy": tftypes.NewValue(tftypes.Bool, false),
		"endpoint": UnknownStr(), "state": UnknownStr(), "created_at": UnknownStr(),
	}
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI, vals map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: Plan(s, vals)}, resp)
	return resp
}

func TestCreateWaitsUntilActive(t *testing.T) {
	api := newFake()
	resp := create(t, api, values(10<<30, true, strSet()))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.State.ValueString() != "active" || m.Region.ValueString() != "af-abj" || m.QuotaBytes.ValueInt64() != 10<<30 ||
		!m.PublicPrefixes.IsNull() || m.CreatedAt.ValueString() != "2026-11-20T09:00:00Z" || m.ForceDestroy.ValueBool() {
		t.Fatalf("model = %+v", m)
	}
	if api.lastCreate.Region != "" || !api.lastCreate.Versioning || api.lastCreate.PublicPrefixes != nil {
		t.Fatalf("sent %+v", api.lastCreate)
	}
}

func TestCreateRefusalIsExplained(t *testing.T) {
	api := newFake()
	api.createErr = &client.APIError{Status: 403, Code: client.CodeObjectStorageNotEnabled}
	resp := create(t, api, values(0, false, strSet()))
	if !strings.Contains(ErrorText(resp.Diagnostics), "closed beta") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestReadDropsADeletedOrDeletingBucket(t *testing.T) {
	api := newFake()
	st := create(t, api, values(0, false, strSet())).State
	api.buckets["bkt_1"].State = client.BucketDeleting
	resp := &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if !IsRemoved(resp.State) {
		t.Fatal("a deleting bucket must leave state")
	}
	delete(api.buckets, "bkt_1")
	resp = &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestUpdateSendsOnlyWhatChanged(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := create(t, api, values(0, false, strSet())).State
	vals := values(0, true, strSet("public/"))
	vals["id"], vals["region"] = Str("bkt_1"), Str("af-abj")
	resp := &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: Plan(s, vals), State: st}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	u := api.lastUpdate
	if u == nil || u.QuotaBytes != nil || u.Versioning == nil || !*u.Versioning || u.PublicPrefixes == nil || strings.Join(*u.PublicPrefixes, ",") != "public/" {
		t.Fatalf("sent %+v", u)
	}
	if m := getModel(t, resp.State); m.State.ValueString() != "active" || len(m.PublicPrefixes.Elements()) != 1 {
		t.Fatalf("model = %+v", m)
	}
}

func TestUpdateOfForceDestroyAloneSendsNothing(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := create(t, api, values(0, false, strSet())).State
	vals := values(0, false, strSet())
	vals["id"], vals["region"], vals["force_destroy"] = Str("bkt_1"), Str("af-abj"), tftypes.NewValue(tftypes.Bool, true)
	resp := &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: Plan(s, vals), State: st}, resp)
	if resp.Diagnostics.HasError() || api.lastUpdate != nil || !getModel(t, resp.State).ForceDestroy.ValueBool() {
		t.Fatalf("diagnostics = %v, sent %+v", resp.Diagnostics, api.lastUpdate)
	}
}

func TestDeletePassesForceDestroyAndToleratesAGoneBucket(t *testing.T) {
	api := newFake()
	vals := values(0, false, strSet())
	vals["force_destroy"] = tftypes.NewValue(tftypes.Bool, true)
	st := create(t, api, vals).State
	for range 2 {
		resp := &resource.DeleteResponse{State: st}
		(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		delete(api.buckets, "bkt_1")
	}
	if api.lastForce == nil || !*api.lastForce {
		t.Fatal("force_destroy was not sent")
	}
}

func TestDeleteOfANonEmptyBucketSaysForceDestroy(t *testing.T) {
	api := newFake()
	st := create(t, api, values(0, false, strSet())).State
	api.deleteErr = &client.APIError{Status: 409, Code: "BUCKET_NOT_EMPTY"}
	resp := &resource.DeleteResponse{State: st}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "force_destroy") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestImportByIDOrName(t *testing.T) {
	api := newFake()
	create(t, api, values(0, false, strSet()))
	s := testSchema(t)
	for _, id := range []string{"bkt_1", "uploads"} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{api: api}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		var got types.String
		resp.State.GetAttribute(Ctx, path.Root("id"), &got)
		if resp.Diagnostics.HasError() || got.ValueString() != "bkt_1" {
			t.Fatalf("%s: diagnostics = %v, id = %v", id, resp.Diagnostics, got)
		}
	}
	resp := &resource.ImportStateResponse{State: EmptyState(s)}
	(&Resource{api: api}).ImportState(Ctx, resource.ImportStateRequest{ID: "Bad_Name"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an invalid id must be refused")
	}
}

func TestNameValidator(t *testing.T) {
	for v, ok := range map[string]bool{"uploads": true, "my-app-2": true, "ab": false, "-ab": false, "Up": false, "a_b": false} {
		var r validator.StringResponse
		nameValidator{}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("name"), ConfigValue: types.StringValue(v)}, &r)
		if r.Diagnostics.HasError() == ok {
			t.Errorf("%q: error = %v", v, r.Diagnostics.HasError())
		}
	}
}

func TestReplaceAndInPlaceAttributes(t *testing.T) {
	s := testSchema(t)
	if a, ok := s.Attributes["name"].(schema.StringAttribute); !ok || len(a.PlanModifiers) == 0 {
		t.Error("name has no RequiresReplace")
	}
	if a, ok := s.Attributes["object_lock"].(schema.BoolAttribute); !ok || len(a.PlanModifiers) == 0 {
		t.Error("object_lock has no RequiresReplace")
	}
	if a, ok := s.Attributes["versioning"].(schema.BoolAttribute); !ok || len(a.PlanModifiers) != 0 {
		t.Error("versioning must change in place")
	}
}
