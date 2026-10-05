package database

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go. PasswordWO is write-only: it is in
// the configuration only, and always null in plan and state.
type model struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	Engine            types.String   `tfsdk:"engine"`
	Version           types.String   `tfsdk:"version"`
	PlanSlug          types.String   `tfsdk:"plan_slug"`
	ZoneID            types.String   `tfsdk:"zone_id"`
	SubnetID          types.String   `tfsdk:"subnet_id"`
	AdminUsername     types.String   `tfsdk:"admin_username"`
	AccessRules       types.Set      `tfsdk:"access_rules"`
	SecurityGroupIDs  types.Set      `tfsdk:"security_group_ids"`
	EffectiveRules    types.List     `tfsdk:"effective_access_rules"`
	IgnoredRules      types.List     `tfsdk:"ignored_security_group_rules"`
	DesiredState      types.String   `tfsdk:"desired_state"`
	PasswordWO        types.String   `tfsdk:"password_wo"`
	PasswordWOVersion types.Int64    `tfsdk:"password_wo_version"`
	PlanID            types.String   `tfsdk:"plan_id"`
	Port              types.Int64    `tfsdk:"port"`
	Hostname          types.String   `tfsdk:"hostname"`
	PrivateIP         types.String   `tfsdk:"private_ip"`
	StorageGB         types.Int64    `tfsdk:"storage_gb"`
	DataVolumeSizeGB  types.Int64    `tfsdk:"data_volume_size_gb"`
	ObservedState     types.String   `tfsdk:"observed_state"`
	FailureCode       types.String   `tfsdk:"failure_code"`
	CreatedAt         types.String   `tfsdk:"created_at"`
	UpdatedAt         types.String   `tfsdk:"updated_at"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

// effectiveRuleType and ignoredRuleType are the object types of the two
// computed lists that explain the allow-list.
var (
	effectiveRuleType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"cidr": types.StringType, "protocol": types.StringType, "port": types.Int64Type, "source": types.StringType,
	}}
	ignoredRuleType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"security_group_id": types.StringType, "direction": types.StringType, "protocol": types.StringType,
		"port_range": types.StringType, "cidr": types.StringType, "reason": types.StringType,
	}}
)

type effectiveRuleModel struct {
	CIDR     types.String `tfsdk:"cidr"`
	Protocol types.String `tfsdk:"protocol"`
	Port     types.Int64  `tfsdk:"port"`
	Source   types.String `tfsdk:"source"`
}

type ignoredRuleModel struct {
	SecurityGroupID types.String `tfsdk:"security_group_id"`
	Direction       types.String `tfsdk:"direction"`
	Protocol        types.String `tfsdk:"protocol"`
	PortRange       types.String `tfsdk:"port_range"`
	CIDR            types.String `tfsdk:"cidr"`
	Reason          types.String `tfsdk:"reason"`
}

// toCreateRequest builds the create body. password comes from the configuration,
// because a write-only value is never in the plan. An unset access_rules is
// omitted so the zone's default applies; an empty set is sent as [].
func toCreateRequest(ctx context.Context, plan model, password string) (client.CreateDatabaseRequest, diag.Diagnostics) {
	req := client.CreateDatabaseRequest{
		Name:          plan.Name.ValueString(),
		Engine:        plan.Engine.ValueString(),
		Version:       plan.Version.ValueString(),
		PlanSlug:      plan.PlanSlug.ValueString(),
		ZoneID:        plan.ZoneID.ValueString(),
		SubnetID:      knownString(plan.SubnetID),
		AdminUsername: knownString(plan.AdminUsername),
		Password:      password,
		StorageGB:     resourcekit.IntPtr(plan.StorageGB),
	}
	if plan.AccessRules.IsNull() || plan.AccessRules.IsUnknown() {
		return req, nil
	}
	cidrs, diags := cidrsOf(ctx, plan.AccessRules)
	req.AccessRules = &cidrs
	return req, diags
}

// cidrsOf reads a set of CIDR strings, sorted so requests are deterministic.
func cidrsOf(ctx context.Context, set types.Set) ([]string, diag.Diagnostics) {
	cidrs := []string{}
	if set.IsNull() || set.IsUnknown() {
		return cidrs, nil
	}
	diags := set.ElementsAs(ctx, &cidrs, false)
	slices.Sort(cidrs)
	return cidrs, diags
}

// fromAPIResponse builds the state from the API object. prev supplies what the
// API does not return: plan_slug (it reports plan_id), the password version and
// the timeouts block. desired_state follows the API when it is running or
// stopped, so a database stopped outside Terraform shows as drift.
func fromAPIResponse(prev model, db *client.Database) (model, diag.Diagnostics) {
	cidrs := make([]string, 0, len(db.AccessRules))
	for _, r := range db.AccessRules {
		cidrs = append(cidrs, r.CIDR)
	}
	ctx := context.Background()
	var diags diag.Diagnostics
	rules, d := types.SetValueFrom(ctx, types.StringType, cidrs)
	diags.Append(d...)
	groups, d := types.SetValueFrom(ctx, types.StringType, nonNil(db.SecurityGroupIDs))
	diags.Append(d...)
	effective, d := types.ListValueFrom(ctx, effectiveRuleType, effectiveRules(db.EffectiveAccessRules))
	diags.Append(d...)
	ignored, d := types.ListValueFrom(ctx, ignoredRuleType, ignoredRules(db.IgnoredSecurityGroupRules))
	diags.Append(d...)

	desired := prev.DesiredState
	if db.DesiredState == client.DatabaseRunning || db.DesiredState == client.DatabaseStopped {
		desired = types.StringValue(db.DesiredState)
	}
	return model{
		ID:                types.StringValue(db.ID),
		Name:              types.StringValue(db.Name),
		Engine:            types.StringValue(db.Engine),
		Version:           types.StringValue(db.Version),
		PlanSlug:          nullIfUnknown(prev.PlanSlug),
		ZoneID:            types.StringValue(db.ZoneID),
		SubnetID:          resourcekit.OptionalString(db.SubnetID),
		AdminUsername:     types.StringValue(db.AdminUsername),
		AccessRules:       rules,
		SecurityGroupIDs:  groups,
		EffectiveRules:    effective,
		IgnoredRules:      ignored,
		DesiredState:      desired,
		PasswordWO:        types.StringNull(),
		PasswordWOVersion: prev.PasswordWOVersion,
		PlanID:            types.StringValue(db.PlanID),
		Port:              types.Int64Value(db.Port),
		Hostname:          resourcekit.OptionalString(db.Hostname),
		PrivateIP:         resourcekit.OptionalString(db.PrivateIP),
		StorageGB:         types.Int64Value(storageOf(db)),
		DataVolumeSizeGB:  types.Int64Value(db.DataVolumeSizeGB),
		ObservedState:     types.StringValue(db.ObservedState),
		FailureCode:       resourcekit.OptionalString(db.FailureCode),
		CreatedAt:         resourcekit.Timestamp(db.CreatedAt),
		UpdatedAt:         resourcekit.Timestamp(db.UpdatedAt),
		Timeouts:          prev.Timeouts,
	}, diags
}

// pendingModel is the state saved the moment the order is accepted. It holds the
// id, so a timeout cannot orphan a paid order, and no unknown values, which
// state may not contain.
func pendingModel(plan model, id, adminUsername string) model {
	m := plan
	m.ID = types.StringValue(id)
	m.PasswordWO = types.StringNull()
	if adminUsername != "" {
		m.AdminUsername = types.StringValue(adminUsername)
	}
	m.AdminUsername = nullIfUnknown(m.AdminUsername)
	if m.AccessRules.IsUnknown() {
		m.AccessRules = types.SetNull(types.StringType)
	}
	if m.SecurityGroupIDs.IsUnknown() {
		m.SecurityGroupIDs = types.SetNull(types.StringType)
	}
	m.EffectiveRules = types.ListNull(effectiveRuleType)
	m.IgnoredRules = types.ListNull(ignoredRuleType)
	m.PlanID = types.StringNull()
	m.Port = types.Int64Null()
	m.Hostname = types.StringNull()
	m.PrivateIP = types.StringNull()
	if m.StorageGB.IsUnknown() {
		m.StorageGB = types.Int64Null()
	}
	m.DataVolumeSizeGB = types.Int64Null()
	m.ObservedState = types.StringNull()
	m.FailureCode = types.StringNull()
	m.CreatedAt = types.StringNull()
	m.UpdatedAt = types.StringNull()
	return m
}

// storageOf is the size the data disk is, or is being grown to. Reporting the
// target of a resize in flight keeps an interrupted apply from sending it again.
func storageOf(db *client.Database) int64 {
	if db.PendingDataVolumeSizeGB != nil && *db.PendingDataVolumeSizeGB > db.DataVolumeSizeGB {
		return *db.PendingDataVolumeSizeGB
	}
	return db.DataVolumeSizeGB
}

func effectiveRules(in []client.DatabaseEffectiveAccessRule) []effectiveRuleModel {
	out := make([]effectiveRuleModel, 0, len(in))
	for _, r := range in {
		out = append(out, effectiveRuleModel{
			CIDR: types.StringValue(r.CIDR), Protocol: types.StringValue(r.Protocol),
			Port: types.Int64Value(r.Port), Source: types.StringValue(r.Source),
		})
	}
	return out
}

func ignoredRules(in []client.DatabaseIgnoredSecurityGroupRule) []ignoredRuleModel {
	out := make([]ignoredRuleModel, 0, len(in))
	for _, r := range in {
		out = append(out, ignoredRuleModel{
			SecurityGroupID: types.StringValue(r.SecurityGroupID), Direction: types.StringValue(r.Direction),
			Protocol: types.StringValue(r.Protocol), PortRange: types.StringPointerValue(r.PortRange),
			CIDR: types.StringValue(r.CIDR), Reason: types.StringValue(r.Reason),
		})
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// knownSet reads a set of strings, sorted, or nil when it is null or unknown.
func knownSet(set types.Set) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	out, _ := cidrsOf(context.Background(), set)
	return out
}

func knownString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func nullIfUnknown(v types.String) types.String {
	if v.IsUnknown() {
		return types.StringNull()
	}
	return v
}
