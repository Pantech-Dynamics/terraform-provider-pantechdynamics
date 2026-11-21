// Package bucket implements the pantechdynamics_bucket resource: an
// S3-compatible object storage bucket (closed beta).
package bucket

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	idPrefix = "bkt_"
	kind     = "bucket"
)

// bucketName is the API's rule: 3 to 63 lower-case letters, digits and
// hyphens, starting and ending with a letter or digit.
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// bucketAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type bucketAPI interface {
	CreateBucket(ctx context.Context, req client.CreateBucketRequest) (*client.Bucket, error)
	GetBucket(ctx context.Context, id string) (*client.Bucket, error)
	ListBuckets(ctx context.Context) ([]client.Bucket, error)
	UpdateBucket(ctx context.Context, id string, req client.UpdateBucketRequest) (*client.Bucket, error)
	DeleteBucket(ctx context.Context, id string, force bool) error
	GetOperation(ctx context.Context, id string) (*client.Operation, error)
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages one bucket.
type Resource struct {
	api bucketAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// model maps the resource's attributes to Go.
type model struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	Region         types.String   `tfsdk:"region"`
	QuotaBytes     types.Int64    `tfsdk:"quota_bytes"`
	Versioning     types.Bool     `tfsdk:"versioning"`
	ObjectLock     types.Bool     `tfsdk:"object_lock"`
	PublicPrefixes types.Set      `tfsdk:"public_prefixes"`
	ForceDestroy   types.Bool     `tfsdk:"force_destroy"`
	Endpoint       types.String   `tfsdk:"endpoint"`
	State          types.String   `tfsdk:"state"`
	CreatedAt      types.String   `tfsdk:"created_at"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the type name: pantechdynamics_bucket.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An S3-compatible object storage bucket. Object storage is in closed beta: an organization outside it is refused with OBJECT_STORAGE_NOT_ENABLED. " +
			"Storage is billed per GiB stored, measured hourly; data out and requests are free during the beta. Reach the bucket with any S3 tool at `endpoint`, " +
			"using a pantechdynamics_object_storage_key. **Destroying a bucket that holds objects fails unless force_destroy is true, which deletes them.**",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the bucket, starting with bkt_.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Bucket name: 3 to 63 lower-case letters, digits and hyphens, starting and ending with a letter or digit. Names are shared by every customer, so it must not be taken. Changing it replaces the bucket.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{nameValidator{}},
			},
			"region": schema.StringAttribute{
				Description:   "Region code, such as \"af-abj\". Leave it out for the default region. Changing it replaces the bucket.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"quota_bytes": schema.Int64Attribute{
				Description: "Size limit in bytes; writes beyond it are refused. 0 (the default) is no limit. Changes in place.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Validators:  []validator.Int64{resourcekit.IntBetween(0, 1<<62)},
			},
			"versioning": schema.BoolAttribute{
				Description: "Keep earlier versions of objects. Defaults to false. Changes in place; turning it off suspends versioning and keeps the versions already stored.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"object_lock": schema.BoolAttribute{
				Description:   "Allow retention on objects. Defaults to false. It cannot be turned off, so changing it replaces the bucket.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"public_prefixes": schema.SetAttribute{
				Description: "Key prefixes anyone may read without a key, such as \"public/\". Leave it out for a wholly private bucket. Changes in place.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"force_destroy": schema.BoolAttribute{
				Description: "When true, destroying the bucket deletes every object in it. When false (the default) a bucket that holds objects cannot be destroyed. Terraform only; not sent to the API until destroy.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"endpoint": schema.StringAttribute{
				Description:   "The S3 endpoint URL for this bucket's region.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"state": schema.StringAttribute{
				Description: "\"provisioning\", \"active\", \"updating\", \"deleting\", \"failed\" or \"unknown\".",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the bucket was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(bucketAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create adds the bucket, saves its id at once, and waits until the store
// holds it.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, plan.Timeouts.Create, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	body, diags := toCreateRequest(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.api.CreateBucket(ctx, body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error creating bucket", err, attributeFor, hints)
		return
	}
	// Saved before the wait, so a bucket that fails or times out is still
	// tracked and the next apply can destroy it.
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ctx, plan, b, &resp.Diagnostics))...)
	final, err := r.waitSettled(ctx, b)
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error creating bucket", kind, b.ID, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ctx, plan, final, &resp.Diagnostics))...)
}

// Read refreshes state from the API. A bucket deleted, or being deleted,
// outside Terraform is removed from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.api.GetBucket(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && b.State == client.BucketDeleting) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading bucket", err, hints)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ctx, state, b, &resp.Diagnostics))...)
}

// Update changes quota_bytes, versioning and public_prefixes in place, and
// waits until the store has applied them.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, plan.Timeouts.Update, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	body, changed, diags := toUpdateRequest(ctx, state, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !changed {
		// Only force_destroy or the timeouts changed: nothing to send.
		state.ForceDestroy, state.Timeouts = plan.ForceDestroy, plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	b, err := r.api.UpdateBucket(ctx, state.ID.ValueString(), body)
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating bucket", err, attributeFor, hints)
		return
	}
	final, err := r.waitSettled(ctx, b)
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error updating bucket", kind, b.ID, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(ctx, plan, final, &resp.Diagnostics))...)
}

// Delete deletes the bucket (and its objects with force_destroy), and waits
// until it reads as deleting or gone. One already gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := resourcekit.WithTimeout(ctx, state.Timeouts.Delete, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	err := r.api.DeleteBucket(ctx, id, state.ForceDestroy.ValueBool())
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error deleting bucket", err, hints)
		return
	}
	err = r.api.WaitUntil(ctx, kind+" "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		b, err := r.api.GetBucket(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return b.State == client.BucketDeleting, nil
	})
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting bucket", kind, id, err)
	}
}

// ImportState adopts a bucket by id (bkt_...) or by name. force_destroy
// starts false.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if !strings.HasPrefix(id, idPrefix) {
		if !bucketName.MatchString(id) {
			resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("Expected a bucket id (bkt_...) or a bucket name, got %q.", id))
			return
		}
		found, err := r.findByName(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Cannot import bucket", err.Error())
			return
		}
		id = found
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)
}

// waitSettled waits until the bucket is active with every change applied. A
// write that needed nothing from the store carries no operation and returns
// at once.
func (r *Resource) waitSettled(ctx context.Context, b *client.Bucket) (*client.Bucket, error) {
	if b.State == client.BucketActive {
		return b, nil
	}
	opID := ""
	if b.OperationID != nil {
		opID = *b.OperationID
	}
	latest := b
	get := func(ctx context.Context) (resourcekit.Status, error) {
		got, err := r.api.GetBucket(ctx, b.ID)
		if err != nil {
			return resourcekit.Status{}, err
		}
		latest = got
		return resourcekit.Status{Observed: got.State}, nil
	}
	err := r.api.WaitUntil(ctx, kind+" "+b.ID+" to be ready", resourcekit.IsActive(get, r.api.GetOperation, kind, b.ID, opID))
	return latest, err
}

// findByName returns the id of the live bucket with exactly this name.
func (r *Resource) findByName(ctx context.Context, name string) (string, error) {
	buckets, err := r.api.ListBuckets(ctx)
	if err != nil {
		return "", err
	}
	for _, b := range buckets {
		if b.Name == name && b.State != client.BucketDeleting {
			return b.ID, nil
		}
	}
	return "", fmt.Errorf("the organization has no bucket named %q", name)
}

// toCreateRequest turns the plan into the create request.
func toCreateRequest(ctx context.Context, plan model) (client.CreateBucketRequest, diag.Diagnostics) {
	req := client.CreateBucketRequest{
		Name:       plan.Name.ValueString(),
		QuotaBytes: plan.QuotaBytes.ValueInt64(),
		Versioning: plan.Versioning.ValueBool(),
		ObjectLock: plan.ObjectLock.ValueBool(),
	}
	if !plan.Region.IsNull() && !plan.Region.IsUnknown() {
		req.Region = plan.Region.ValueString()
	}
	prefixes, diags := setStrings(ctx, plan.PublicPrefixes)
	req.PublicPrefixes = prefixes
	return req, diags
}

// toUpdateRequest sends only what differs from state.
func toUpdateRequest(ctx context.Context, state, plan model) (client.UpdateBucketRequest, bool, diag.Diagnostics) {
	var req client.UpdateBucketRequest
	changed := false
	if !plan.QuotaBytes.Equal(state.QuotaBytes) {
		q := plan.QuotaBytes.ValueInt64()
		req.QuotaBytes, changed = &q, true
	}
	if !plan.Versioning.Equal(state.Versioning) {
		v := plan.Versioning.ValueBool()
		req.Versioning, changed = &v, true
	}
	want, diags := setStrings(ctx, plan.PublicPrefixes)
	had, d := setStrings(ctx, state.PublicPrefixes)
	diags.Append(d...)
	if !sameSet(want, had) {
		if want == nil {
			want = []string{}
		}
		req.PublicPrefixes, changed = &want, true
	}
	return req, changed, diags
}

// fromAPIResponse builds the state. force_destroy and timeouts are Terraform's
// own, so they come from prev. public_prefixes keeps the configured value
// (null or empty) when it means the same as what the API reports.
func fromAPIResponse(ctx context.Context, prev model, b *client.Bucket, diags *diag.Diagnostics) model {
	force := prev.ForceDestroy
	if force.IsNull() || force.IsUnknown() {
		force = types.BoolValue(false)
	}
	return model{
		ID:             types.StringValue(b.ID),
		Name:           types.StringValue(b.Name),
		Region:         types.StringValue(b.Region),
		QuotaBytes:     types.Int64Value(b.QuotaBytes),
		Versioning:     types.BoolValue(b.Versioning),
		ObjectLock:     types.BoolValue(b.ObjectLock),
		PublicPrefixes: statePrefixes(ctx, prev.PublicPrefixes, b.PublicPrefixes, diags),
		ForceDestroy:   force,
		Endpoint:       types.StringValue(b.Endpoint),
		State:          types.StringValue(b.State),
		CreatedAt:      resourcekit.Timestamp(b.CreatedAt),
		Timeouts:       prev.Timeouts,
	}
}

func statePrefixes(ctx context.Context, prev types.Set, reported []string, diags *diag.Diagnostics) types.Set {
	if !prev.IsUnknown() {
		configured, d := setStrings(ctx, prev)
		diags.Append(d...)
		if sameSet(configured, reported) {
			return prev
		}
	}
	if len(reported) == 0 {
		return types.SetNull(types.StringType)
	}
	set, d := types.SetValueFrom(ctx, types.StringType, reported)
	diags.Append(d...)
	return set
}

// setStrings reads a set of strings; null and unknown are nil.
func setStrings(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := s.ElementsAs(ctx, &out, false)
	slices.Sort(out)
	return out, diags
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// attributeFor maps the fields a 422 can name to schema attributes.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "region", "quota_bytes", "versioning", "object_lock", "public_prefixes":
		return path.Root(field), true
	}
	return path.Path{}, false
}

// hints explain the refusals a user can act on.
var hints = map[string]string{
	client.CodeObjectStorageNotEnabled:  "Object storage is in closed beta and is not switched on for your organization. Ask support to join the beta.",
	"BUCKET_NAME_TAKEN":                 "Bucket names are shared by every customer, and this one is taken. Choose another name.",
	"BUCKET_LIMIT_REACHED":              "Your organization already has the most buckets it may have. Delete one you no longer use.",
	"BUCKET_NOT_EMPTY":                  "The bucket still holds objects. Set force_destroy = true and apply, then destroy again to delete them with it.",
	"BUCKET_RETENTION":                  "Objects in the bucket are under retention and cannot be deleted yet.",
	"OBJECT_STORAGE_REGION_UNAVAILABLE": "Object storage is not offered in that region.",
	"OBJECT_STORAGE_UNAVAILABLE":        "Object storage is not answering right now. Try again shortly.",
	"ACCOUNT_SUSPENDED":                 "Your organization is suspended. Settle the balance in the console, then try again.",
	"ACCOUNT_NOT_ACTIVE":                "Billing setup is not finished. Finish it in the console, then try again.",
	"FORBIDDEN":                         "The API key's role may not change object storage. Use a key created by an owner or admin.",
	"INSUFFICIENT_SCOPE":                "Changing buckets needs an API key with the write scope.",
	"INSUFFICIENT_DELETE_SCOPE":         "Deleting a bucket needs an API key with the delete scope.",
}

// nameValidator checks the bucket naming rule at plan time.
type nameValidator struct{}

func (nameValidator) Description(context.Context) string {
	return "must be 3 to 63 lower-case letters, digits and hyphens, starting and ending with a letter or digit"
}

func (v nameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v nameValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !bucketName.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid bucket name", fmt.Sprintf("%q %s.", req.ConfigValue.ValueString(), v.Description(ctx)))
	}
}
