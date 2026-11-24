package bucket

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// bucketRules carries the sub-resource configurations stored alongside a bucket.
type bucketRules struct {
	tags       []client.BucketTag
	cors       []client.BucketCORSRule
	lifecycle  []client.BucketLifecycleRule
	objectLock *client.BucketObjectLock
}

type corsRuleModel struct {
	ID             types.String `tfsdk:"id"`
	AllowedOrigins types.List   `tfsdk:"allowed_origins"`
	AllowedMethods types.List   `tfsdk:"allowed_methods"`
	AllowedHeaders types.List   `tfsdk:"allowed_headers"`
	ExposeHeaders  types.List   `tfsdk:"expose_headers"`
	MaxAgeSeconds  types.Int64  `tfsdk:"max_age_seconds"`
}

type lifecycleRuleModel struct {
	ID                           types.String `tfsdk:"id"`
	Prefix                       types.String `tfsdk:"prefix"`
	Enabled                      types.Bool   `tfsdk:"enabled"`
	ExpirationDays               types.Int64  `tfsdk:"expiration_days"`
	NoncurrentExpirationDays     types.Int64  `tfsdk:"noncurrent_expiration_days"`
	AbortIncompleteMultipartDays types.Int64  `tfsdk:"abort_incomplete_multipart_days"`
	ExpiredObjectDeleteMarker    types.Bool   `tfsdk:"expired_object_delete_marker"`
}

type defaultObjectLockModel struct {
	Enabled types.Bool   `tfsdk:"enabled"`
	Mode    types.String `tfsdk:"mode"`
	Days    types.Int64  `tfsdk:"days"`
	Years   types.Int64  `tfsdk:"years"`
}

var (
	corsRuleAttrTypes = map[string]attr.Type{
		"id":              types.StringType,
		"allowed_origins": types.ListType{ElemType: types.StringType},
		"allowed_methods": types.ListType{ElemType: types.StringType},
		"allowed_headers": types.ListType{ElemType: types.StringType},
		"expose_headers":  types.ListType{ElemType: types.StringType},
		"max_age_seconds": types.Int64Type,
	}
	corsRuleObjectType = types.ObjectType{AttrTypes: corsRuleAttrTypes}

	lifecycleRuleAttrTypes = map[string]attr.Type{
		"id":                              types.StringType,
		"prefix":                          types.StringType,
		"enabled":                         types.BoolType,
		"expiration_days":                 types.Int64Type,
		"noncurrent_expiration_days":      types.Int64Type,
		"abort_incomplete_multipart_days": types.Int64Type,
		"expired_object_delete_marker":    types.BoolType,
	}
	lifecycleRuleObjectType = types.ObjectType{AttrTypes: lifecycleRuleAttrTypes}

	defaultObjectLockAttrTypes = map[string]attr.Type{
		"enabled": types.BoolType,
		"mode":    types.StringType,
		"days":    types.Int64Type,
		"years":   types.Int64Type,
	}

	validCORSMethods = []string{"GET", "PUT", "POST", "DELETE", "HEAD"}
)

// ValidateConfig checks cross-attribute constraints at plan time:
//   - cors_rules[*].allowed_methods must be valid HTTP methods;
//   - default_object_lock requires object_lock = true and, when enabled, a valid
//     mode and either days or years (not both).
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var objectLock types.Bool
	var defaultLock types.Object
	var corsRules types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("object_lock"), &objectLock)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("default_object_lock"), &defaultLock)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cors_rules"), &corsRules)...)
	if resp.Diagnostics.HasError() {
		return
	}

	validateCORSRules(ctx, corsRules, &resp.Diagnostics)
	validateDefaultObjectLock(ctx, objectLock, defaultLock, &resp.Diagnostics)
}

func validateCORSRules(ctx context.Context, corsRules types.List, diags *diag.Diagnostics) {
	if corsRules.IsNull() || corsRules.IsUnknown() {
		return
	}
	var rules []corsRuleModel
	diags.Append(corsRules.ElementsAs(ctx, &rules, false)...)
	if diags.HasError() {
		return
	}
	for i, rule := range rules {
		if rule.AllowedMethods.IsNull() || rule.AllowedMethods.IsUnknown() {
			continue
		}
		var methods []types.String
		diags.Append(rule.AllowedMethods.ElementsAs(ctx, &methods, false)...)
		if diags.HasError() {
			return
		}
		for j, m := range methods {
			if m.IsNull() || m.IsUnknown() {
				continue
			}
			if !slices.Contains(validCORSMethods, m.ValueString()) {
				diags.AddAttributeError(
					path.Root("cors_rules").AtListIndex(i).AtName("allowed_methods").AtListIndex(j),
					"Invalid CORS method",
					fmt.Sprintf("Expected one of %s, got %q.", strings.Join(validCORSMethods, ", "), m.ValueString()),
				)
			}
		}
	}
}

func validateDefaultObjectLock(ctx context.Context, objectLock types.Bool, defaultLock types.Object, diags *diag.Diagnostics) {
	if defaultLock.IsNull() {
		return
	}
	if objectLock.IsNull() || (!objectLock.IsUnknown() && !objectLock.ValueBool()) {
		diags.AddAttributeError(path.Root("default_object_lock"), "default_object_lock requires object_lock = true",
			"Default object-lock retention can only be configured on a bucket created with object_lock = true.")
	}
	if defaultLock.IsUnknown() {
		return
	}
	var cfg defaultObjectLockModel
	diags.Append(defaultLock.As(ctx, &cfg, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return
	}
	if cfg.Enabled.IsUnknown() {
		return
	}
	// Enabled defaults to true when omitted from default_object_lock.
	enabled := cfg.Enabled.IsNull() || cfg.Enabled.ValueBool()
	if !enabled {
		return
	}
	if !cfg.Mode.IsUnknown() {
		mode := ""
		if !cfg.Mode.IsNull() {
			mode = cfg.Mode.ValueString()
		}
		if mode != client.ObjectLockModeGovernance && mode != client.ObjectLockModeCompliance {
			diags.AddAttributeError(path.Root("default_object_lock").AtName("mode"), "Invalid object lock mode",
				fmt.Sprintf("When default_object_lock is enabled, mode must be %q or %q, got %q.", client.ObjectLockModeGovernance, client.ObjectLockModeCompliance, mode))
		}
	}
	if cfg.Days.IsUnknown() || cfg.Years.IsUnknown() {
		return
	}
	days := int64(0)
	if !cfg.Days.IsNull() {
		days = cfg.Days.ValueInt64()
	}
	years := int64(0)
	if !cfg.Years.IsNull() {
		years = cfg.Years.ValueInt64()
	}
	switch {
	case days > 0 && years > 0:
		diags.AddAttributeError(path.Root("default_object_lock"), "Set either days or years, not both",
			"Default object-lock retention accepts either days or years, not both.")
	case days <= 0 && years <= 0:
		diags.AddAttributeError(path.Root("default_object_lock"), "Retention period required",
			"When default_object_lock is enabled, set either days or years to a positive integer.")
	}
}

func mapToTags(ctx context.Context, m types.Map) ([]client.BucketTag, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return nil, nil
	}
	var raw map[string]string
	diags := m.ElementsAs(ctx, &raw, false)
	if diags.HasError() || len(raw) == 0 {
		return nil, diags
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	tags := make([]client.BucketTag, 0, len(keys))
	for _, k := range keys {
		tags = append(tags, client.BucketTag{Key: k, Value: raw[k]})
	}
	return tags, diags
}

func sortTags(tags []client.BucketTag) []client.BucketTag {
	out := slices.Clone(tags)
	slices.SortFunc(out, func(a, b client.BucketTag) int {
		if a.Key != b.Key {
			return strings.Compare(a.Key, b.Key)
		}
		return strings.Compare(a.Value, b.Value)
	})
	return out
}

func sameTags(a, b []client.BucketTag) bool {
	return slices.Equal(sortTags(a), sortTags(b))
}

func stateTags(ctx context.Context, prev types.Map, reported []client.BucketTag, diags *diag.Diagnostics) types.Map {
	if !prev.IsUnknown() {
		configured, d := mapToTags(ctx, prev)
		diags.Append(d...)
		if sameTags(configured, reported) {
			if prev.IsNull() {
				return types.MapNull(types.StringType)
			}
			return prev
		}
	}
	if len(reported) == 0 {
		return types.MapNull(types.StringType)
	}
	m := make(map[string]string, len(reported))
	for _, t := range reported {
		m[t.Key] = t.Value
	}
	val, d := types.MapValueFrom(ctx, types.StringType, m)
	diags.Append(d...)
	return val
}

func listToCORSRules(ctx context.Context, l types.List) ([]client.BucketCORSRule, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var items []corsRuleModel
	diags := l.ElementsAs(ctx, &items, false)
	if diags.HasError() || len(items) == 0 {
		return nil, diags
	}
	out := make([]client.BucketCORSRule, 0, len(items))
	for _, it := range items {
		origins, d := listStrings(ctx, it.AllowedOrigins)
		diags.Append(d...)
		methods, d := listStrings(ctx, it.AllowedMethods)
		diags.Append(d...)
		headers, d := listStrings(ctx, it.AllowedHeaders)
		diags.Append(d...)
		expose, d := listStrings(ctx, it.ExposeHeaders)
		diags.Append(d...)
		out = append(out, client.BucketCORSRule{
			ID:             knownStr(it.ID),
			AllowedOrigins: origins,
			AllowedMethods: methods,
			AllowedHeaders: headers,
			ExposeHeaders:  expose,
			MaxAgeSeconds:  knownInt(it.MaxAgeSeconds),
		})
	}
	return out, diags
}

func sameCORSRules(a, b []client.BucketCORSRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].MaxAgeSeconds != b[i].MaxAgeSeconds ||
			!slices.Equal(a[i].AllowedOrigins, b[i].AllowedOrigins) ||
			!slices.Equal(a[i].AllowedMethods, b[i].AllowedMethods) ||
			!slices.Equal(a[i].AllowedHeaders, b[i].AllowedHeaders) ||
			!slices.Equal(a[i].ExposeHeaders, b[i].ExposeHeaders) {
			return false
		}
	}
	return true
}

func stateCORSRules(ctx context.Context, prev types.List, reported []client.BucketCORSRule, diags *diag.Diagnostics) types.List {
	if !prev.IsUnknown() {
		configured, d := listToCORSRules(ctx, prev)
		diags.Append(d...)
		if sameCORSRules(configured, reported) {
			if prev.IsNull() {
				return types.ListNull(corsRuleObjectType)
			}
			return prev
		}
	}
	if len(reported) == 0 {
		return types.ListNull(corsRuleObjectType)
	}
	elems := make([]attr.Value, 0, len(reported))
	for _, r := range reported {
		origins, d := stringListValue(ctx, r.AllowedOrigins)
		diags.Append(d...)
		methods, d := stringListValue(ctx, r.AllowedMethods)
		diags.Append(d...)
		headers, d := stringListValue(ctx, r.AllowedHeaders)
		diags.Append(d...)
		expose, d := stringListValue(ctx, r.ExposeHeaders)
		diags.Append(d...)
		obj, d := types.ObjectValue(corsRuleAttrTypes, map[string]attr.Value{
			"id":              types.StringValue(r.ID),
			"allowed_origins": origins,
			"allowed_methods": methods,
			"allowed_headers": headers,
			"expose_headers":  expose,
			"max_age_seconds": types.Int64Value(r.MaxAgeSeconds),
		})
		diags.Append(d...)
		elems = append(elems, obj)
	}
	val, d := types.ListValue(corsRuleObjectType, elems)
	diags.Append(d...)
	return val
}

func listToLifecycleRules(ctx context.Context, l types.List) ([]client.BucketLifecycleRule, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var items []lifecycleRuleModel
	diags := l.ElementsAs(ctx, &items, false)
	if diags.HasError() || len(items) == 0 {
		return nil, diags
	}
	out := make([]client.BucketLifecycleRule, 0, len(items))
	for _, it := range items {
		enabled := true
		if !it.Enabled.IsNull() && !it.Enabled.IsUnknown() {
			enabled = it.Enabled.ValueBool()
		}
		out = append(out, client.BucketLifecycleRule{
			ID:                           knownStr(it.ID),
			Prefix:                       knownStr(it.Prefix),
			Enabled:                      enabled,
			ExpirationDays:               knownInt(it.ExpirationDays),
			NoncurrentExpirationDays:     knownInt(it.NoncurrentExpirationDays),
			AbortIncompleteMultipartDays: knownInt(it.AbortIncompleteMultipartDays),
			ExpiredObjectDeleteMarker:    !it.ExpiredObjectDeleteMarker.IsNull() && !it.ExpiredObjectDeleteMarker.IsUnknown() && it.ExpiredObjectDeleteMarker.ValueBool(),
		})
	}
	return out, diags
}

func stateLifecycleRules(ctx context.Context, prev types.List, reported []client.BucketLifecycleRule, diags *diag.Diagnostics) types.List {
	if !prev.IsUnknown() {
		configured, d := listToLifecycleRules(ctx, prev)
		diags.Append(d...)
		if slices.Equal(configured, reported) {
			if prev.IsNull() {
				return types.ListNull(lifecycleRuleObjectType)
			}
			return prev
		}
	}
	if len(reported) == 0 {
		return types.ListNull(lifecycleRuleObjectType)
	}
	elems := make([]attr.Value, 0, len(reported))
	for _, r := range reported {
		obj, d := types.ObjectValue(lifecycleRuleAttrTypes, map[string]attr.Value{
			"id":                              types.StringValue(r.ID),
			"prefix":                          types.StringValue(r.Prefix),
			"enabled":                         types.BoolValue(r.Enabled),
			"expiration_days":                 types.Int64Value(r.ExpirationDays),
			"noncurrent_expiration_days":      types.Int64Value(r.NoncurrentExpirationDays),
			"abort_incomplete_multipart_days": types.Int64Value(r.AbortIncompleteMultipartDays),
			"expired_object_delete_marker":    types.BoolValue(r.ExpiredObjectDeleteMarker),
		})
		diags.Append(d...)
		elems = append(elems, obj)
	}
	val, d := types.ListValue(lifecycleRuleObjectType, elems)
	diags.Append(d...)
	return val
}

func objectToObjectLock(ctx context.Context, o types.Object) (client.BucketObjectLock, bool, diag.Diagnostics) {
	if o.IsNull() || o.IsUnknown() {
		return client.BucketObjectLock{}, false, nil
	}
	var cfg defaultObjectLockModel
	diags := o.As(ctx, &cfg, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return client.BucketObjectLock{}, false, diags
	}
	enabled := cfg.Enabled.IsNull() || cfg.Enabled.IsUnknown() || cfg.Enabled.ValueBool()
	return client.BucketObjectLock{
		Enabled: enabled,
		Mode:    knownStr(cfg.Mode),
		Days:    knownInt(cfg.Days),
		Years:   knownInt(cfg.Years),
	}, true, diags
}

func stateObjectLock(ctx context.Context, prev types.Object, reported *client.BucketObjectLock, diags *diag.Diagnostics) types.Object {
	if reported == nil || !reported.Enabled {
		if prev.IsNull() || prev.IsUnknown() {
			return types.ObjectNull(defaultObjectLockAttrTypes)
		}
		cfg, set, d := objectToObjectLock(ctx, prev)
		diags.Append(d...)
		if set && !cfg.Enabled {
			return prev
		}
		return types.ObjectNull(defaultObjectLockAttrTypes)
	}
	obj, d := types.ObjectValue(defaultObjectLockAttrTypes, map[string]attr.Value{
		"enabled": types.BoolValue(reported.Enabled),
		"mode":    types.StringValue(reported.Mode),
		"days":    types.Int64Value(reported.Days),
		"years":   types.Int64Value(reported.Years),
	})
	diags.Append(d...)
	return obj
}

func listStrings(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return []string{}, nil
	}
	var out []string
	diags := l.ElementsAs(ctx, &out, false)
	if out == nil {
		out = []string{}
	}
	return out, diags
}

func stringListValue(ctx context.Context, vals []string) (types.List, diag.Diagnostics) {
	if vals == nil {
		vals = []string{}
	}
	return types.ListValueFrom(ctx, types.StringType, vals)
}

func knownStr(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func knownInt(v types.Int64) int64 {
	if v.IsNull() || v.IsUnknown() {
		return 0
	}
	return v.ValueInt64()
}
