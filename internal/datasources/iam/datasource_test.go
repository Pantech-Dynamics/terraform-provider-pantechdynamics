package iam

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/accesspolicy"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	policies []client.IAMPolicy
	actions  []client.IAMAction
}

func (f *fakeAPI) ListIAMPolicies(_ context.Context, managed *bool) ([]client.IAMPolicy, error) {
	if managed != nil {
		panic("the lookup must list both kinds")
	}
	return f.policies, nil
}

func (f *fakeAPI) ListIAMActions(context.Context) ([]client.IAMAction, error) { return f.actions, nil }

func mfa(b bool) *bool { return &b }

func fixture() *fakeAPI {
	return &fakeAPI{
		policies: []client.IAMPolicy{
			{ID: "pol_pantech_require_mfa_for_writes", Name: "pantech_require_mfa_for_writes", Managed: true,
				Statements: []client.PolicyStatement{{Sid: "DenyWritesWithoutMFA", Effect: "deny", Actions: []string{"*:Delete"}, Resources: []string{"*"}, Conditions: &client.PolicyConditions{MFA: mfa(false)}}}},
			{ID: "pol_1", Name: "staging-operators", Statements: []client.PolicyStatement{{Effect: "allow", Actions: []string{"instances:Stop"}, Resources: []string{"name:staging-*"}}}},
		},
		actions: []client.IAMAction{
			{Action: "instances:Stop", Service: "instances", Description: "Stop.", ResourceTypes: []string{"instance"}},
			{Action: "billing:BuyCredit", Service: "billing", Description: "Buy credit."},
		},
	}
}

// config builds a configuration value; blocks are statement block values.
func config(t *testing.T, ds datasource.DataSource, set map[string]tftypes.Value) (tfsdk.Config, tftypes.Object) {
	t.Helper()
	var sresp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &sresp)
	if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	typ, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}, typ
}

func read(t *testing.T, ds datasource.DataSource, set map[string]tftypes.Value) datasource.ReadResponse {
	t.Helper()
	cfg, typ := config(t, ds, set)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: cfg.Schema, Raw: tftypes.NewValue(typ, nil)}}
	if v, ok := ds.(datasource.DataSourceWithValidateConfig); ok {
		var vresp datasource.ValidateConfigResponse
		v.ValidateConfig(ctx, datasource.ValidateConfigRequest{Config: cfg}, &vresp)
		resp.Diagnostics.Append(vresp.Diagnostics...)
		if resp.Diagnostics.HasError() {
			return resp
		}
	}
	ds.Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	return resp
}

func errText(resp datasource.ReadResponse) string {
	var b strings.Builder
	for _, e := range resp.Diagnostics.Errors() {
		b.WriteString(e.Summary() + " " + e.Detail() + "\n")
	}
	return b.String()
}

func TestPolicyLookupFindsManagedPolicies(t *testing.T) {
	resp := read(t, &PolicyDataSource{api: fixture()}, map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.String, "pol_pantech_require_mfa_for_writes")})
	var m policyModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var statements []accesspolicy.StatementModel
	m.Statements.ElementsAs(ctx, &statements, false)
	if !m.Managed.ValueBool() || len(statements) != 1 || statements[0].Conditions == nil || statements[0].Conditions.MFA.ValueBool() {
		t.Fatalf("model = %+v, statements = %+v", m, statements)
	}
	if !strings.Contains(m.PolicyJSON.ValueString(), `"mfa": false`) {
		t.Fatalf("policy_json = %s", m.PolicyJSON.ValueString())
	}
	resp = read(t, &PolicyDataSource{api: fixture()}, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "staging-operators")})
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if m.ID.ValueString() != "pol_1" || m.Managed.ValueBool() {
		t.Fatalf("model = %+v", m)
	}
	resp = read(t, &PolicyDataSource{api: fixture()}, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "nope")})
	if !strings.Contains(errText(resp), "Available names") {
		t.Fatalf("errors = %s", errText(resp))
	}
}

func TestActionsFilterByService(t *testing.T) {
	resp := read(t, &ActionsDataSource{api: fixture()}, map[string]tftypes.Value{"service": tftypes.NewValue(tftypes.String, "billing")})
	var m actionsModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || len(m.Actions) != 1 || m.Actions[0].Action.ValueString() != "billing:BuyCredit" || m.Actions[0].ResourceTypes.IsNull() || m.ID.ValueString() != "iam_actions/billing" {
		t.Fatalf("diagnostics = %v, model = %+v", resp.Diagnostics, m)
	}
	resp = read(t, &ActionsDataSource{api: fixture()}, nil)
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if len(m.Actions) != 2 || m.ID.ValueString() != "iam_actions" {
		t.Fatalf("model = %+v", m)
	}
}

func statementBlocks(t *testing.T, actions []string, cidrs ...string) tftypes.Value {
	t.Helper()
	_, typ := config(t, &DocumentDataSource{}, nil)
	list, ok := typ.AttributeTypes["statement"].(tftypes.List)
	if !ok {
		t.Fatal("statement is not a list")
	}
	obj, ok := list.ElementType.(tftypes.Object)
	if !ok {
		t.Fatal("statement element is not an object")
	}
	set := func(vals ...string) tftypes.Value {
		elems := []tftypes.Value{}
		for _, v := range vals {
			elems = append(elems, tftypes.NewValue(tftypes.String, v))
		}
		return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
	}
	cond := tftypes.NewValue(obj.AttributeTypes["conditions"], nil)
	if cidrs != nil {
		cond = tftypes.NewValue(obj.AttributeTypes["conditions"], map[string]tftypes.Value{"source_ip": set(cidrs...), "mfa": tftypes.NewValue(tftypes.Bool, nil)})
	}
	block := tftypes.NewValue(obj, map[string]tftypes.Value{
		"sid": tftypes.NewValue(tftypes.String, "Ops"), "effect": tftypes.NewValue(tftypes.String, "allow"),
		"actions": set(actions...), "resources": set("name:staging-*"), "conditions": cond,
	})
	return tftypes.NewValue(list, []tftypes.Value{block})
}

// The document is the normal form: sorted, de-duplicated, CIDRs canonical.
func TestDocumentBuildsNormalizedJSON(t *testing.T) {
	resp := read(t, &DocumentDataSource{}, map[string]tftypes.Value{"statement": statementBlocks(t, []string{"instances:Stop", "instances:Start"}, "102.89.1.2")})
	var m documentModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	doc := m.JSON.ValueString()
	if !strings.Contains(doc, `"102.89.1.2/32"`) || strings.Index(doc, "instances:Start") > strings.Index(doc, "instances:Stop") || !strings.Contains(doc, `"sid": "Ops"`) {
		t.Fatalf("json = %s", doc)
	}
	statements, err := accesspolicy.ParseDocument(doc)
	if err != nil || len(statements) != 1 {
		t.Fatalf("statements = %+v, err = %v", statements, err)
	}
}

func TestDocumentRefusesInvalidStatements(t *testing.T) {
	for _, tt := range []struct {
		actions []string
		cidrs   []string
		want    string
	}{
		{[]string{"Instances:Stop"}, nil, "the service"},
		{[]string{"instances:Stop"}, []string{"300.0.0.0/8"}, "CIDR"},
	} {
		resp := read(t, &DocumentDataSource{}, map[string]tftypes.Value{"statement": statementBlocks(t, tt.actions, tt.cidrs...)})
		if !strings.Contains(errText(resp), tt.want) {
			t.Errorf("errors = %s, want %q", errText(resp), tt.want)
		}
	}
	// No statements at all.
	resp := read(t, &DocumentDataSource{}, nil)
	if !strings.Contains(errText(resp), "1 to 20 statements") {
		t.Fatalf("errors = %s", errText(resp))
	}
}
