package containerrepository

import (
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go. Retention is a pointer to a
// struct, which the framework maps to a nested object; nil is no policy.
type model struct {
	ID            types.String    `tfsdk:"id"`
	Name          types.String    `tfsdk:"name"`
	Description   types.String    `tfsdk:"description"`
	Visibility    types.String    `tfsdk:"visibility"`
	ImmutableTags types.Bool      `tfsdk:"immutable_tags"`
	Retention     *retentionModel `tfsdk:"retention"`
	Namespace     types.String    `tfsdk:"namespace"`
	FullName      types.String    `tfsdk:"full_name"`
	TagCount      types.Int64     `tfsdk:"tag_count"`
	StorageBytes  types.Int64     `tfsdk:"storage_bytes"`
	LastPushedAt  types.String    `tfsdk:"last_pushed_at"`
	Status        types.String    `tfsdk:"status"`
	CreatedAt     types.String    `tfsdk:"created_at"`
	Timeouts      timeouts.Value  `tfsdk:"timeouts"`
}

// retentionModel is the cleanup policy. A null field is no rule.
type retentionModel struct {
	KeepLast          types.Int64  `tfsdk:"keep_last"`
	UntaggedAfterDays types.Int64  `tfsdk:"untagged_after_days"`
	KeepPattern       types.String `tfsdk:"keep_pattern"`
}

// toAPIRetention turns the block into the API policy; nil stays nil.
func toAPIRetention(r *retentionModel) *client.RepositoryRetention {
	if r == nil {
		return nil
	}
	return &client.RepositoryRetention{
		KeepLast:          resourcekit.IntPtr(r.KeepLast),
		UntaggedAfterDays: resourcekit.IntPtr(r.UntaggedAfterDays),
		KeepPattern:       r.KeepPattern.ValueStringPointer(),
	}
}

// fromAPIRetention turns the API policy into the block. A policy with no rule
// is no block, so a configuration without retention stays stable.
func fromAPIRetention(r *client.RepositoryRetention) *retentionModel {
	if r.IsEmpty() {
		return nil
	}
	return &retentionModel{
		KeepLast:          resourcekit.OptionalInt64(r.KeepLast),
		UntaggedAfterDays: resourcekit.OptionalInt64(r.UntaggedAfterDays),
		KeepPattern:       types.StringPointerValue(r.KeepPattern),
	}
}

func sameRetention(a, b *retentionModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.KeepLast.Equal(b.KeepLast) && a.UntaggedAfterDays.Equal(b.UntaggedAfterDays) && a.KeepPattern.Equal(b.KeepPattern)
}

// toCreateRequest turns the plan into the create request.
func toCreateRequest(plan model) client.CreateRepositoryRequest {
	immutable := plan.ImmutableTags.ValueBool()
	return client.CreateRepositoryRequest{
		Name:          plan.Name.ValueString(),
		Description:   plan.Description.ValueString(),
		Visibility:    plan.Visibility.ValueString(),
		ImmutableTags: &immutable,
		Retention:     toAPIRetention(plan.Retention),
	}
}

// toUpdateRequest sends only what changed. Removing the retention block sends
// a policy of nulls, which removes every rule. ok is false when nothing
// changed.
func toUpdateRequest(state, plan model) (client.UpdateRepositoryRequest, bool) {
	var req client.UpdateRepositoryRequest
	if !plan.Description.Equal(state.Description) {
		req.Description = plan.Description.ValueStringPointer()
	}
	if !plan.Visibility.Equal(state.Visibility) {
		req.Visibility = plan.Visibility.ValueStringPointer()
	}
	if !plan.ImmutableTags.Equal(state.ImmutableTags) {
		req.ImmutableTags = plan.ImmutableTags.ValueBoolPointer()
	}
	if !sameRetention(plan.Retention, state.Retention) {
		req.Retention = toAPIRetention(plan.Retention)
		if req.Retention == nil {
			req.Retention = &client.RepositoryRetention{}
		}
	}
	changed := req.Description != nil || req.Visibility != nil || req.ImmutableTags != nil || req.Retention != nil
	return req, changed
}

// fromAPIResponse builds the state. The timeouts block is Terraform's alone,
// so it is carried over.
func fromAPIResponse(r *client.Repository, prevTimeouts timeouts.Value) model {
	return model{
		ID:            types.StringValue(r.ID),
		Name:          types.StringValue(r.Name),
		Description:   types.StringValue(r.Description),
		Visibility:    types.StringValue(r.Visibility),
		ImmutableTags: types.BoolValue(r.ImmutableTags),
		Retention:     fromAPIRetention(r.Retention),
		Namespace:     types.StringValue(r.Namespace),
		FullName:      types.StringValue(r.FullName),
		TagCount:      types.Int64Value(r.TagCount),
		StorageBytes:  types.Int64Value(r.StorageBytes),
		LastPushedAt:  resourcekit.Timestamp(r.LastPushedAt),
		Status:        types.StringValue(r.Status),
		CreatedAt:     resourcekit.Timestamp(r.CreatedAt),
		Timeouts:      prevTimeouts,
	}
}
