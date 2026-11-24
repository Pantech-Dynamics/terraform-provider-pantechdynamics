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

// fakeAPI keeps buckets and their rules in memory. A new bucket reads as
// provisioning once, then active, the way the store settles it.
type fakeAPI struct {
	buckets    map[string]*client.Bucket
	tags       map[string][]client.BucketTag
	cors       map[string][]client.BucketCORSRule
	lifecycle  map[string][]client.BucketLifecycleRule
	objectLock map[string]client.BucketObjectLock

	reads          map[string]int
	createErr      error
	deleteErr      error
	lastCreate     client.CreateBucketRequest
	lastUpdate     *client.UpdateBucketRequest
	lastForce      *bool
	tagPuts        int
	corsPuts       int
	lifecyclePuts  int
	objectLockPuts int
}

func newFake() *fakeAPI {
	return &fakeAPI{
		buckets:    map[string]*client.Bucket{},
		tags:       map[string][]client.BucketTag{},
		cors:       map[string][]client.BucketCORSRule{},
		lifecycle:  map[string][]client.BucketLifecycleRule{},
		objectLock: map[string]client.BucketObjectLock{},
		reads:      map[string]int{},
	}
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

func (f *fakeAPI) GetBucketCORS(_ context.Context, id string) ([]client.BucketCORSRule, error) {
	return append([]client.BucketCORSRule{}, f.cors[id]...), nil
}

func (f *fakeAPI) PutBucketCORS(_ context.Context, id string, rules []client.BucketCORSRule) ([]client.BucketCORSRule, error) {
	f.corsPuts++
	f.cors[id] = append([]client.BucketCORSRule{}, rules...)
	return append([]client.BucketCORSRule{}, rules...), nil
}

func (f *fakeAPI) GetBucketLifecycle(_ context.Context, id string) ([]client.BucketLifecycleRule, error) {
	return append([]client.BucketLifecycleRule{}, f.lifecycle[id]...), nil
}

func (f *fakeAPI) PutBucketLifecycle(_ context.Context, id string, rules []client.BucketLifecycleRule) ([]client.BucketLifecycleRule, error) {
	f.lifecyclePuts++
	f.lifecycle[id] = append([]client.BucketLifecycleRule{}, rules...)
	return append([]client.BucketLifecycleRule{}, rules...), nil
}

func (f *fakeAPI) GetBucketTags(_ context.Context, id string) ([]client.BucketTag, error) {
	return append([]client.BucketTag{}, f.tags[id]...), nil
}

func (f *fakeAPI) PutBucketTags(_ context.Context, id string, tags []client.BucketTag) ([]client.BucketTag, error) {
	f.tagPuts++
	f.tags[id] = append([]client.BucketTag{}, tags...)
	return append([]client.BucketTag{}, tags...), nil
}

func (f *fakeAPI) GetBucketObjectLock(_ context.Context, id string) (*client.BucketObjectLock, error) {
	ol := f.objectLock[id]
	return &ol, nil
}

func (f *fakeAPI) PutBucketObjectLock(_ context.Context, id string, cfg client.BucketObjectLock) (*client.BucketObjectLock, error) {
	f.objectLockPuts++
	f.objectLock[id] = cfg
	cp := cfg
	return &cp, nil
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

func strList(vals ...string) tftypes.Value {
	typ := tftypes.List{ElementType: tftypes.String}
	elems := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, Str(v))
	}
	return tftypes.NewValue(typ, elems)
}

func strMap(m map[string]string) tftypes.Value {
	typ := tftypes.Map{ElementType: tftypes.String}
	if m == nil {
		return tftypes.NewValue(typ, nil)
	}
	vals := make(map[string]tftypes.Value, len(m))
	for k, v := range m {
		vals[k] = Str(v)
	}
	return tftypes.NewValue(typ, vals)
}

var (
	corsRuleTFType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"id":              tftypes.String,
		"allowed_origins": tftypes.List{ElementType: tftypes.String},
		"allowed_methods": tftypes.List{ElementType: tftypes.String},
		"allowed_headers": tftypes.List{ElementType: tftypes.String},
		"expose_headers":  tftypes.List{ElementType: tftypes.String},
		"max_age_seconds": tftypes.Number,
	}}
	lifecycleRuleTFType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"id":                              tftypes.String,
		"prefix":                          tftypes.String,
		"enabled":                         tftypes.Bool,
		"expiration_days":                 tftypes.Number,
		"noncurrent_expiration_days":      tftypes.Number,
		"abort_incomplete_multipart_days": tftypes.Number,
		"expired_object_delete_marker":    tftypes.Bool,
	}}
	defaultObjectLockTFType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"enabled": tftypes.Bool,
		"mode":    tftypes.String,
		"days":    tftypes.Number,
		"years":   tftypes.Number,
	}}
)

func corsRulesTF(rules ...map[string]tftypes.Value) tftypes.Value {
	typ := tftypes.List{ElementType: corsRuleTFType}
	elems := make([]tftypes.Value, 0, len(rules))
	for _, r := range rules {
		elems = append(elems, tftypes.NewValue(corsRuleTFType, r))
	}
	return tftypes.NewValue(typ, elems)
}

func lifecycleRulesTF(rules ...map[string]tftypes.Value) tftypes.Value {
	typ := tftypes.List{ElementType: lifecycleRuleTFType}
	elems := make([]tftypes.Value, 0, len(rules))
	for _, r := range rules {
		elems = append(elems, tftypes.NewValue(lifecycleRuleTFType, r))
	}
	return tftypes.NewValue(typ, elems)
}

func objectLockTF(enabled bool, mode string, days, years int64) tftypes.Value {
	return tftypes.NewValue(defaultObjectLockTFType, map[string]tftypes.Value{
		"enabled": tftypes.NewValue(tftypes.Bool, enabled),
		"mode":    Str(mode),
		"days":    Num(days),
		"years":   Num(years),
	})
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
	if api.tagPuts+api.corsPuts+api.lifecyclePuts+api.objectLockPuts != 0 {
		t.Fatalf("unset optional rules must not trigger PUTs: tags=%d cors=%d lifecycle=%d lock=%d",
			api.tagPuts, api.corsPuts, api.lifecyclePuts, api.objectLockPuts)
	}
	if !m.Tags.IsNull() || !m.CORSRules.IsNull() || !m.LifecycleRules.IsNull() || !m.DefaultObjectLock.IsNull() {
		t.Fatalf("unset optional rules must stay null in state: %+v", m)
	}
}

func TestCreateReadUpdateAndClearInlineRules(t *testing.T) {
	api := newFake()
	s := testSchema(t)

	vals := values(0, true, strSet())
	vals["object_lock"] = tftypes.NewValue(tftypes.Bool, true)
	vals["tags"] = strMap(map[string]string{"env": "prod", "team": "core"})
	vals["cors_rules"] = corsRulesTF(map[string]tftypes.Value{
		"id":              Str("web"),
		"allowed_origins": strList("https://app.example.com"),
		"allowed_methods": strList("GET", "PUT"),
		"allowed_headers": strList("*"),
		"expose_headers":  strList("ETag"),
		"max_age_seconds": Num(3600),
	})
	vals["lifecycle_rules"] = lifecycleRulesTF(map[string]tftypes.Value{
		"id":                              Str("expire-logs"),
		"prefix":                          Str("logs/"),
		"enabled":                         tftypes.NewValue(tftypes.Bool, true),
		"expiration_days":                 Num(30),
		"noncurrent_expiration_days":      Num(7),
		"abort_incomplete_multipart_days": Num(3),
		"expired_object_delete_marker":    tftypes.NewValue(tftypes.Bool, false),
	})
	vals["default_object_lock"] = objectLockTF(true, client.ObjectLockModeGovernance, 30, 0)

	cresp := create(t, api, vals)
	if cresp.Diagnostics.HasError() {
		t.Fatal(ErrorText(cresp.Diagnostics))
	}
	if api.tagPuts != 1 || api.corsPuts != 1 || api.lifecyclePuts != 1 || api.objectLockPuts != 1 {
		t.Fatalf("puts: tags=%d cors=%d lifecycle=%d lock=%d", api.tagPuts, api.corsPuts, api.lifecyclePuts, api.objectLockPuts)
	}

	// Read back and verify all rules survive cleanly.
	rresp := &resource.ReadResponse{State: cresp.State}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: cresp.State}, rresp)
	if rresp.Diagnostics.HasError() {
		t.Fatal(ErrorText(rresp.Diagnostics))
	}
	rm := getModel(t, rresp.State)
	if len(rm.Tags.Elements()) != 2 || len(rm.CORSRules.Elements()) != 1 || len(rm.LifecycleRules.Elements()) != 1 || rm.DefaultObjectLock.IsNull() {
		t.Fatalf("read model = %+v", rm)
	}

	// Update rules in place without patching core bucket settings.
	uvals := values(0, true, strSet())
	uvals["id"], uvals["region"], uvals["object_lock"] = Str("bkt_1"), Str("af-abj"), tftypes.NewValue(tftypes.Bool, true)
	uvals["tags"] = strMap(map[string]string{"env": "staging"})
	uvals["cors_rules"] = corsRulesTF(map[string]tftypes.Value{
		"id":              Str("web"),
		"allowed_origins": strList("*"),
		"allowed_methods": strList("GET"),
		"allowed_headers": strList(),
		"expose_headers":  strList(),
		"max_age_seconds": Num(600),
	})
	uvals["lifecycle_rules"] = lifecycleRulesTF(map[string]tftypes.Value{
		"id":                              Str("expire-logs"),
		"prefix":                          Str("logs/"),
		"enabled":                         tftypes.NewValue(tftypes.Bool, false),
		"expiration_days":                 Num(90),
		"noncurrent_expiration_days":      Num(0),
		"abort_incomplete_multipart_days": Num(0),
		"expired_object_delete_marker":    tftypes.NewValue(tftypes.Bool, true),
	})
	uvals["default_object_lock"] = objectLockTF(true, client.ObjectLockModeCompliance, 0, 1)

	uresp := &resource.UpdateResponse{State: rresp.State}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: Plan(s, uvals), State: rresp.State}, uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(ErrorText(uresp.Diagnostics))
	}
	if api.lastUpdate != nil {
		t.Fatalf("core bucket should not have been patched: %+v", api.lastUpdate)
	}
	if api.tagPuts != 2 || api.corsPuts != 2 || api.lifecyclePuts != 2 || api.objectLockPuts != 2 {
		t.Fatalf("puts after update: tags=%d cors=%d lifecycle=%d lock=%d", api.tagPuts, api.corsPuts, api.lifecyclePuts, api.objectLockPuts)
	}

	// Clear all optional rule attributes by setting them back to null.
	clearVals := values(0, true, strSet())
	clearVals["id"], clearVals["region"], clearVals["object_lock"] = Str("bkt_1"), Str("af-abj"), tftypes.NewValue(tftypes.Bool, true)
	c2resp := &resource.UpdateResponse{State: uresp.State}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: Plan(s, clearVals), State: uresp.State}, c2resp)
	if c2resp.Diagnostics.HasError() {
		t.Fatal(ErrorText(c2resp.Diagnostics))
	}
	if api.tagPuts != 3 || api.corsPuts != 3 || api.lifecyclePuts != 3 || api.objectLockPuts != 3 {
		t.Fatalf("puts after clear: tags=%d cors=%d lifecycle=%d lock=%d", api.tagPuts, api.corsPuts, api.lifecyclePuts, api.objectLockPuts)
	}
	cm := getModel(t, c2resp.State)
	if !cm.Tags.IsNull() || !cm.CORSRules.IsNull() || !cm.LifecycleRules.IsNull() || !cm.DefaultObjectLock.IsNull() {
		t.Fatalf("cleared attributes must be null in state: %+v", cm)
	}
	if len(api.tags["bkt_1"]) != 0 || len(api.cors["bkt_1"]) != 0 || len(api.lifecycle["bkt_1"]) != 0 || api.objectLock["bkt_1"].Enabled {
		t.Fatalf("backend rules should all be cleared")
	}
}

func TestValidateConfigBucketRules(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
		wantSub string
	}{
		{
			"default_object_lock valid with days",
			map[string]tftypes.Value{"name": Str("uploads"), "object_lock": tftypes.NewValue(tftypes.Bool, true), "default_object_lock": objectLockTF(true, client.ObjectLockModeGovernance, 30, 0)},
			false, "",
		},
		{
			"default_object_lock valid with years",
			map[string]tftypes.Value{"name": Str("uploads"), "object_lock": tftypes.NewValue(tftypes.Bool, true), "default_object_lock": objectLockTF(true, client.ObjectLockModeCompliance, 0, 2)},
			false, "",
		},
		{
			"default_object_lock without object_lock",
			map[string]tftypes.Value{"name": Str("uploads"), "object_lock": tftypes.NewValue(tftypes.Bool, false), "default_object_lock": objectLockTF(true, client.ObjectLockModeGovernance, 30, 0)},
			true, "requires object_lock = true",
		},
		{
			"default_object_lock with both days and years",
			map[string]tftypes.Value{"name": Str("uploads"), "object_lock": tftypes.NewValue(tftypes.Bool, true), "default_object_lock": objectLockTF(true, client.ObjectLockModeGovernance, 30, 1)},
			true, "not both",
		},
		{
			"default_object_lock with neither days nor years",
			map[string]tftypes.Value{"name": Str("uploads"), "object_lock": tftypes.NewValue(tftypes.Bool, true), "default_object_lock": objectLockTF(true, client.ObjectLockModeGovernance, 0, 0)},
			true, "positive integer",
		},
		{
			"default_object_lock missing mode when enabled",
			map[string]tftypes.Value{"name": Str("uploads"), "object_lock": tftypes.NewValue(tftypes.Bool, true), "default_object_lock": objectLockTF(true, "", 30, 0)},
			true, "Invalid object lock mode",
		},
		{
			"default_object_lock disabled skips mode and duration",
			map[string]tftypes.Value{"name": Str("uploads"), "object_lock": tftypes.NewValue(tftypes.Bool, true), "default_object_lock": objectLockTF(false, "", 0, 0)},
			false, "",
		},
		{
			"invalid cors method",
			map[string]tftypes.Value{"name": Str("uploads"), "cors_rules": corsRulesTF(map[string]tftypes.Value{
				"id": Str("bad"), "allowed_origins": strList("*"), "allowed_methods": strList("PATCH"),
				"allowed_headers": strList(), "expose_headers": strList(), "max_age_seconds": Num(0),
			})},
			true, "Invalid CORS method",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp resource.ValidateConfigResponse
			(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: Object(s, tt.set)}}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s, wantErr = %v", ErrorText(resp.Diagnostics), tt.wantErr)
			}
			if tt.wantSub != "" && !strings.Contains(ErrorText(resp.Diagnostics), tt.wantSub) {
				t.Fatalf("diags = %s, want %q", ErrorText(resp.Diagnostics), tt.wantSub)
			}
		})
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
