package database

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

const testPassword = "Correct-Horse-Battery-9"

var setType = tftypes.Set{ElementType: tftypes.String}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func cidrSet(cidrs ...string) tftypes.Value {
	vals := make([]tftypes.Value, 0, len(cidrs))
	for _, c := range cidrs {
		vals = append(vals, Str(c))
	}
	return tftypes.NewValue(setType, vals)
}

// args are the arguments as configured. Computed attributes are added as
// unknown for a plan.
func args(desired string, rules tftypes.Value) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"name": Str("orders"), "engine": Str("postgresql"), "version": Str("18"), "plan_slug": Str("starter"),
		"zone_id": Str("af-abj-2"), "subnet_id": Str("snet_1"), "admin_username": Str("dbadmin"),
		"access_rules": rules, "desired_state": Str(desired),
	}
}

func planFor(s schema.Schema, a map[string]tftypes.Value) tfsdk.Plan {
	vals := map[string]tftypes.Value{
		"id": UnknownStr(), "plan_id": UnknownStr(), "port": Unknown(tftypes.Number), "hostname": UnknownStr(),
		"private_ip": UnknownStr(), "data_volume_size_gb": Unknown(tftypes.Number), "observed_state": UnknownStr(),
		"failure_code": UnknownStr(), "created_at": UnknownStr(), "updated_at": UnknownStr(),
		"effective_access_rules": Unknown(types.ListType{ElemType: effectiveRuleType}.TerraformType(Ctx)), "ignored_security_group_rules": Unknown(types.ListType{ElemType: ignoredRuleType}.TerraformType(Ctx)),
	}
	if _, ok := a["security_group_ids"]; !ok {
		vals["security_group_ids"] = Unknown(setType)
	}
	for k, v := range a {
		vals[k] = v
	}
	return Plan(s, vals)
}

// configFor is the configuration, the only place the write-only password is.
func configFor(s schema.Schema, a map[string]tftypes.Value, password string) tfsdk.Config {
	vals := map[string]tftypes.Value{"password_wo": Str(password)}
	for k, v := range a {
		vals[k] = v
	}
	return tfsdk.Config{Schema: s, Raw: Object(s, vals)}
}

func stateFor(s schema.Schema, desired string, version tftypes.Value) tfsdk.State {
	vals := args(desired, cidrSet("10.0.0.0/16"))
	vals["id"] = Str("db_1")
	vals["password_wo_version"] = version
	vals["plan_id"] = Str("plan_1")
	vals["port"] = Num(5432)
	vals["observed_state"] = Str(desired)
	vals["security_group_ids"] = cidrSet()
	return State(s, vals)
}

func seeded(observed string) *fakeAPI {
	api := &fakeAPI{}
	api.dbs = []client.Database{{
		ID: "db_1", Name: "orders", Engine: "postgresql", Version: "18", Port: 5432, PlanID: "plan_1", ZoneID: "af-abj-2",
		SubnetID: ptr("snet_1"), AdminUsername: "dbadmin", DesiredState: observed, ObservedState: observed,
		Generation: 3, ObservedGeneration: 3, AccessRules: toRules([]string{"10.0.0.0/16"}),
	}}
	return api
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI, a map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, a), Config: configFor(s, a, testPassword)}, resp)
	return resp
}

func TestPasswordIsWriteOnlyAndSensitive(t *testing.T) {
	attr := testSchema(t).Attributes["password_wo"]
	if !attr.IsWriteOnly() || !attr.IsSensitive() || !attr.IsRequired() {
		t.Fatalf("password_wo must be required, write-only and sensitive: %+v", attr)
	}
}

func TestCreateSendsThePasswordButNeverStoresIt(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, args("running", tftypes.NewValue(setType, nil)))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.lastCreate.Password != testPassword || api.lastCreate.ZoneID != "af-abj-2" || api.lastCreate.SubnetID != "snet_1" {
		t.Errorf("create = %+v", api.lastCreate)
	}
	if api.lastCreate.AccessRules != nil {
		t.Errorf("access_rules sent = %v, want omitted for the zone default", *api.lastCreate.AccessRules)
	}
	m := getModel(t, resp.State)
	if !m.PasswordWO.IsNull() {
		t.Error("password_wo is in state")
	}
	if strings.Contains(resp.State.Raw.String(), testPassword) {
		t.Error("the password appears in the state")
	}
	if m.ID.ValueString() != "db_1" || m.Port.ValueInt64() != 5432 || m.Hostname.IsNull() || m.ObservedState.ValueString() != "running" {
		t.Errorf("model = %+v", m)
	}
	if m.PlanSlug.ValueString() != "starter" || m.AdminUsername.ValueString() != "dbadmin" || m.CreatedAt.ValueString() != "2026-10-05T10:00:00Z" {
		t.Errorf("model = %+v", m)
	}
	cidrs, _ := cidrsOf(Ctx, m.AccessRules)
	if !slices.Equal(cidrs, []string{"10.0.0.0/16"}) {
		t.Errorf("access_rules = %v, want the zone default the API reported", cidrs)
	}
}

func TestCreateSendsConfiguredAccessRules(t *testing.T) {
	tests := []struct {
		name  string
		rules tftypes.Value
		want  []string
	}{
		{"two rules", cidrSet("192.168.0.0/24", "10.1.0.0/16"), []string{"10.1.0.0/16", "192.168.0.0/24"}},
		{"none", cidrSet(), []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{}
			if resp := create(t, api, args("running", tt.rules)); resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if api.lastCreate.AccessRules == nil || !slices.Equal(*api.lastCreate.AccessRules, tt.want) {
				t.Errorf("access_rules sent = %v, want %v", api.lastCreate.AccessRules, tt.want)
			}
		})
	}
}

func TestCreateRefusesADuplicateNameBeforeOrdering(t *testing.T) {
	api := seeded("running")
	resp := create(t, api, args("running", cidrSet()))
	if !resp.Diagnostics.HasError() || !strings.Contains(ErrorText(resp.Diagnostics), "already exists") {
		t.Fatalf("diags = %v", resp.Diagnostics)
	}
	if api.creates != 0 {
		t.Errorf("creates = %d, want no order", api.creates)
	}
}

func TestCreateFailedOrderKeepsTheID(t *testing.T) {
	api := &fakeAPI{orderErr: &client.DatabaseOrderError{Order: client.DatabaseOrder{ID: "ord_1", Status: client.OrderPaymentFailed, FailureCode: ptr("card_declined")}}}
	resp := create(t, api, args("running", cidrSet()))
	text := ErrorText(resp.Diagnostics)
	if !strings.Contains(text, "card_declined") || !strings.Contains(text, "db_1") {
		t.Fatalf("diags = %s", text)
	}
	if m := getModel(t, resp.State); m.ID.ValueString() != "db_1" {
		t.Errorf("id = %s, want it saved so destroy can clean up", m.ID)
	}
	if strings.Contains(text, testPassword) {
		t.Error("the password appears in a diagnostic")
	}
}

func TestCreateFailedDatabaseReportsTheCode(t *testing.T) {
	api := &fakeAPI{createState: client.DatabaseFailed}
	resp := create(t, api, args("running", cidrSet()))
	if !strings.Contains(ErrorText(resp.Diagnostics), "failed state") {
		t.Fatalf("diags = %v", resp.Diagnostics)
	}
}

func TestCreateInsufficientCredit(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 402, Code: client.CodeInsufficientCredit, Detail: "needs NGN 15,040.00"}}
	resp := create(t, api, args("running", cidrSet()))
	if !strings.Contains(ErrorText(resp.Diagnostics), "Not enough credit") {
		t.Fatalf("diags = %v", resp.Diagnostics)
	}
}

func TestCreateStoppedStopsAfterProvisioning(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, args("stopped", cidrSet()))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.stops != 1 || getModel(t, resp.State).ObservedState.ValueString() != "stopped" {
		t.Errorf("stops = %d, state = %+v", api.stops, getModel(t, resp.State))
	}
}

func TestCreateWithoutAPasswordIsRefused(t *testing.T) {
	s := testSchema(t)
	a := args("running", cidrSet())
	resp := &resource.CreateResponse{State: EmptyState(s)}
	cfg := tfsdk.Config{Schema: s, Raw: Object(s, a)} // password_wo null
	api := &fakeAPI{}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, a), Config: cfg}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "Missing database password") || api.creates != 0 {
		t.Fatalf("diags = %v, creates = %d", resp.Diagnostics, api.creates)
	}
}

func TestCreateAttachesSecurityGroupsAfterProvisioning(t *testing.T) {
	api := &fakeAPI{}
	a := args("running", cidrSet("10.0.0.0/16"))
	a["security_group_ids"] = cidrSet("sg_2", "sg_1")
	resp := create(t, api, a)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !slices.Equal(api.actionOrder, []string{"create", "groups"}) || !slices.Equal(api.lastGroups, []string{"sg_1", "sg_2"}) {
		t.Fatalf("actions = %v, groups = %v", api.actionOrder, api.lastGroups)
	}
	m := getModel(t, resp.State)
	if len(m.SecurityGroupIDs.Elements()) != 2 || len(m.EffectiveRules.Elements()) != 3 || len(m.IgnoredRules.Elements()) != 2 {
		t.Errorf("model = %+v", m)
	}
}

func TestCreateWithoutSecurityGroupsSendsNone(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, args("running", cidrSet()))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.groupCalls != 0 {
		t.Errorf("group calls = %d", api.groupCalls)
	}
	if m := getModel(t, resp.State); m.SecurityGroupIDs.IsNull() || len(m.SecurityGroupIDs.Elements()) != 0 {
		t.Errorf("security_group_ids = %v, want the API's empty set", m.SecurityGroupIDs)
	}
}

func TestUpdateSecurityGroups(t *testing.T) {
	api := seeded("running")
	s := testSchema(t)
	a := args("running", cidrSet("10.0.0.0/16"))
	a["security_group_ids"] = cidrSet("sg_9")
	resp := update(t, api, stateFor(s, "running", Num(1)), a, Num(1), testPassword)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !slices.Equal(api.actionOrder, []string{"groups"}) || !slices.Equal(api.lastGroups, []string{"sg_9"}) {
		t.Errorf("actions = %v, groups = %v", api.actionOrder, api.lastGroups)
	}
}

// errDatabaseNotFound is how the API answers a GET or DELETE of a deleted
// database: its own code, not RESOURCE_NOT_FOUND.
var errDatabaseNotFound = fmt.Errorf("getting database: %w", &client.APIError{Status: 404, Code: "DATABASE_NOT_FOUND", Detail: "No database with this id in your organization."})

func TestRead(t *testing.T) {
	tests := []struct {
		name        string
		api         *fakeAPI
		wantRemoved bool
		wantDesired string
	}{
		{"running", seeded("running"), false, "running"},
		{"stopped outside terraform", seeded("stopped"), false, "stopped"},
		{"gone", &fakeAPI{}, true, ""},
		{"gone, answered with DATABASE_NOT_FOUND", &fakeAPI{getErr: errDatabaseNotFound}, true, ""},
		{"deleted but readable", seeded(client.DatabaseDeleted), true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSchema(t)
			resp := &resource.ReadResponse{State: stateFor(s, "running", Num(1))}
			(&Resource{api: tt.api}).Read(Ctx, resource.ReadRequest{State: stateFor(s, "running", Num(1))}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if IsRemoved(resp.State) != tt.wantRemoved {
				t.Fatalf("removed = %v", IsRemoved(resp.State))
			}
			if tt.wantRemoved {
				return
			}
			m := getModel(t, resp.State)
			if m.DesiredState.ValueString() != tt.wantDesired || m.PasswordWOVersion.ValueInt64() != 1 || m.PlanSlug.ValueString() != "starter" {
				t.Errorf("model = %+v", m)
			}
		})
	}
}

func TestReadReportsOtherErrors(t *testing.T) {
	s := testSchema(t)
	api := &fakeAPI{getErr: errors.New("boom")}
	resp := &resource.ReadResponse{State: stateFor(s, "running", Num(1))}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s, "running", Num(1))}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
}

func update(t *testing.T, api *fakeAPI, state tfsdk.State, a map[string]tftypes.Value, version tftypes.Value, password string) *resource.UpdateResponse {
	t.Helper()
	s := testSchema(t)
	a["password_wo_version"] = version
	plan := planFor(s, a)
	resp := &resource.UpdateResponse{State: state}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: state, Config: configFor(s, a, password)}, resp)
	return resp
}

func TestUpdateReplacesTheWholeAllowList(t *testing.T) {
	api := seeded("running")
	s := testSchema(t)
	resp := update(t, api, stateFor(s, "running", Num(1)), args("running", cidrSet("10.0.0.0/16", "172.16.0.0/12")), Num(1), testPassword)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.ruleCalls != 1 || !slices.Equal(api.lastRules, []string{"10.0.0.0/16", "172.16.0.0/12"}) {
		t.Errorf("rule calls = %d, rules = %v", api.ruleCalls, api.lastRules)
	}
	if len(api.passwords) != 0 {
		t.Errorf("password sent %d times, want none: the version did not change", len(api.passwords))
	}
	cidrs, _ := cidrsOf(Ctx, getModel(t, resp.State).AccessRules)
	if len(cidrs) != 2 {
		t.Errorf("access_rules = %v", cidrs)
	}
}

func TestUpdateUnchangedRulesSendNothing(t *testing.T) {
	api := seeded("running")
	s := testSchema(t)
	resp := update(t, api, stateFor(s, "running", Num(1)), args("running", cidrSet("10.0.0.0/16")), Num(1), testPassword)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(api.actionOrder) != 0 {
		t.Errorf("actions = %v, want none", api.actionOrder)
	}
}

// A password change needs a running database: a stopped one is started, changed
// and stopped again.
func TestUpdatePasswordVersionSetsThePassword(t *testing.T) {
	api := seeded("stopped")
	s := testSchema(t)
	resp := update(t, api, stateFor(s, "stopped", Num(1)), args("stopped", cidrSet("10.0.0.0/16")), Num(2), "Another-Password-Of-24")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !slices.Equal(api.actionOrder, []string{"start", "password", "stop"}) {
		t.Errorf("actions = %v", api.actionOrder)
	}
	if !slices.Equal(api.passwords, []string{"Another-Password-Of-24"}) {
		t.Errorf("passwords = %v", api.passwords)
	}
	m := getModel(t, resp.State)
	if m.PasswordWOVersion.ValueInt64() != 2 || !m.PasswordWO.IsNull() || m.DesiredState.ValueString() != "stopped" {
		t.Errorf("model = %+v", m)
	}
}

func TestUpdatePasswordFailureKeepsTheOldVersion(t *testing.T) {
	api := seeded("running")
	api.passwordErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "password", Code: "INVALID", Message: "refused"}}}
	s := testSchema(t)
	resp := update(t, api, stateFor(s, "running", Num(1)), args("running", cidrSet("10.0.0.0/16")), Num(2), testPassword)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if d := resp.Diagnostics.Errors()[0]; d == nil || strings.Contains(d.Detail(), testPassword) {
		t.Errorf("diagnostic = %v", d)
	}
	if m := getModel(t, resp.State); m.PasswordWOVersion.ValueInt64() != 1 {
		t.Errorf("password_wo_version = %v, want 1 so the next apply retries", m.PasswordWOVersion)
	}
}

func TestUpdatePowerState(t *testing.T) {
	api := seeded("running")
	s := testSchema(t)
	resp := update(t, api, stateFor(s, "running", Num(1)), args("stopped", cidrSet("10.0.0.0/16")), Num(1), testPassword)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !slices.Equal(api.actionOrder, []string{"stop"}) {
		t.Errorf("actions = %v", api.actionOrder)
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name string
		api  *fakeAPI
	}{
		{"waits until deleted", seeded("running")},
		{"already gone", &fakeAPI{}},
		{"already gone, answered with DATABASE_NOT_FOUND", &fakeAPI{deleteErr: errDatabaseNotFound}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSchema(t)
			resp := &resource.DeleteResponse{State: stateFor(s, "running", Num(1))}
			(&Resource{api: tt.api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s, "running", Num(1))}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
		})
	}
}

func TestImportChecksThePrefix(t *testing.T) {
	s := testSchema(t)
	for id, wantErr := range map[string]bool{"db_1": false, "vm_1": true} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		resp.State.Raw = Object(s, nil)
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%s: diags = %v", id, resp.Diagnostics)
		}
	}
}

func TestAttributeForMapsThePasswordField(t *testing.T) {
	if p, ok := attributeFor("password"); !ok || !p.Equal(path.Root("password_wo")) {
		t.Errorf("password -> %v, %v", p, ok)
	}
	if _, ok := attributeFor("bogus"); ok {
		t.Error("bogus must not map")
	}
}
