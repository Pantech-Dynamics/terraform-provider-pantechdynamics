package accesspolicy

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/accesspolicy"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI stores statements the way the backend does: actions and resources
// de-duplicated in the order sent, CIDRs masked to their network.
type fakeAPI struct {
	policies    map[string]client.IAMPolicy
	catalogue   []client.IAMAction
	createErr   error
	updateErr   error
	lastIfMatch int64
	updates     int
}

func newFake() *fakeAPI {
	return &fakeAPI{policies: map[string]client.IAMPolicy{}, catalogue: []client.IAMAction{
		{Action: "instances:Read"}, {Action: "instances:Start"}, {Action: "instances:Stop"}, {Action: "instances:Delete"}, {Action: "dns:Read"},
	}}
}

func stored(in []client.PolicyStatement) []client.PolicyStatement {
	out := make([]client.PolicyStatement, 0, len(in))
	for _, s := range in {
		s.Actions = slices.Compact(slices.Clone(s.Actions))
		if s.Conditions != nil && s.Conditions.SourceIP != nil {
			c := *s.Conditions
			c.SourceIP = nil
			for _, raw := range s.Conditions.SourceIP {
				canon, _ := accesspolicy.CanonicalCIDR(raw)
				c.SourceIP = append(c.SourceIP, canon)
			}
			s.Conditions = &c
		}
		out = append(out, s)
	}
	return out
}

func (f *fakeAPI) CreateIAMPolicy(_ context.Context, req client.IAMPolicyRequest) (*client.IAMPolicy, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	p := client.IAMPolicy{ID: "pol_1", Name: req.Name, Description: req.Description, Statements: stored(req.Statements), Version: 1}
	f.policies[p.ID] = p
	return &p, nil
}

func (f *fakeAPI) GetIAMPolicy(_ context.Context, id string) (*client.IAMPolicy, error) {
	p, ok := f.policies[id]
	if !ok {
		return nil, fmt.Errorf("getting policy: %w", &client.APIError{Status: 404, Code: client.CodeIAMPolicyNotFound})
	}
	return &p, nil
}

func (f *fakeAPI) UpdateIAMPolicy(_ context.Context, id string, version int64, req client.IAMPolicyRequest) (*client.IAMPolicy, error) {
	f.updates++
	f.lastIfMatch = version
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	p := f.policies[id]
	if p.Version != version {
		return nil, &client.APIError{Status: 412, Code: client.CodeVersionConflict}
	}
	p.Name, p.Description, p.Statements, p.Version = req.Name, req.Description, stored(req.Statements), p.Version+1
	f.policies[id] = p
	return &p, nil
}

func (f *fakeAPI) DeleteIAMPolicy(_ context.Context, id string) error {
	if _, ok := f.policies[id]; !ok {
		return &client.APIError{Status: 404, Code: client.CodeIAMPolicyNotFound}
	}
	delete(f.policies, id)
	return nil
}

func (f *fakeAPI) ListIAMActions(context.Context) ([]client.IAMAction, error) {
	return f.catalogue, nil
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func blockType(s schema.Schema) tftypes.Object {
	typ, ok := s.Blocks["statement"].Type().TerraformType(Ctx).(tftypes.List)
	if !ok {
		panic("statement is not a list")
	}
	obj, ok := typ.ElementType.(tftypes.Object)
	if !ok {
		panic("statement element is not an object")
	}
	return obj
}

func strSet(vals ...string) tftypes.Value {
	elems := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, Str(v))
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
}

// block is one statement block; cidrs nil means no conditions.
func block(s schema.Schema, actions []string, cidrs ...string) tftypes.Value {
	obj := blockType(s)
	condType := obj.AttributeTypes["conditions"]
	cond := tftypes.NewValue(condType, nil)
	if cidrs != nil {
		cond = tftypes.NewValue(condType, map[string]tftypes.Value{"source_ip": strSet(cidrs...), "mfa": tftypes.NewValue(tftypes.Bool, nil)})
	}
	return tftypes.NewValue(obj, map[string]tftypes.Value{
		"sid": tftypes.NewValue(tftypes.String, nil), "effect": Str("allow"), "actions": strSet(actions...), "resources": strSet("name:staging-*"), "conditions": cond,
	})
}

func blocks(s schema.Schema, vals ...tftypes.Value) tftypes.Value {
	return tftypes.NewValue(tftypes.List{ElementType: blockType(s)}, vals)
}

func planWith(s schema.Schema, id tftypes.Value, stmts, policyJSON tftypes.Value) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": id, "name": Str("staging-operators"), "description": Str(""), "statement": stmts, "policy_json": policyJSON,
		"attachment_count": Unknown(tftypes.Number), "version": Unknown(tftypes.Number), "created_at": UnknownStr(),
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

func create(t *testing.T, api *fakeAPI, plan tfsdk.Plan) *resource.CreateResponse {
	t.Helper()
	resp := &resource.CreateResponse{State: EmptyState(testSchema(t))}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: plan}, resp)
	return resp
}

// The backend stores 102.89.1.2 as 102.89.1.2/32; state keeps the configured
// block, so the next plan shows no change.
func TestCreateWithBlocksKeepsTheConfiguredSpelling(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	resp := create(t, api, planWith(s, UnknownStr(), blocks(s, block(s, []string{"instances:Stop", "instances:Start"}, "102.89.1.2")), UnknownStr()))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	var cidrs []string
	m.Statements[0].Conditions.SourceIP.ElementsAs(Ctx, &cidrs, false)
	if len(m.Statements) != 1 || cidrs[0] != "102.89.1.2" || m.Version.ValueInt64() != 1 {
		t.Fatalf("model = %+v, cidrs = %v", m, cidrs)
	}
	if !strings.Contains(m.PolicyJSON.ValueString(), `"102.89.1.2/32"`) {
		t.Fatalf("policy_json = %s, want the normal form", m.PolicyJSON.ValueString())
	}
}

func TestCreateWithPolicyJSONKeepsTheConfiguredText(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	text := `{"statements":[{"effect":"deny","resources":["*"],"actions":["instances:Delete","instances:Delete"]}]}`
	resp := create(t, api, planWith(s, UnknownStr(), blocks(s), Str(text)))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.PolicyJSON.ValueString() != text || len(m.Statements) != 0 {
		t.Fatalf("model = %+v", m)
	}
}

// After an import there is no previous form, so the statements come back as
// blocks.
func TestReadAfterImportFillsBlocks(t *testing.T) {
	api := newFake()
	api.policies["pol_1"] = client.IAMPolicy{ID: "pol_1", Name: "p", Statements: []client.PolicyStatement{{Effect: "allow", Actions: []string{"dns:Read"}, Resources: []string{"*"}}}, Version: 3}
	s := testSchema(t)
	imported := State(s, map[string]tftypes.Value{"id": Str("pol_1"), "statement": blocks(s)})
	resp := &resource.ReadResponse{State: imported}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: imported}, resp)
	m := getModel(t, resp.State)
	if resp.Diagnostics.HasError() || len(m.Statements) != 1 || m.Statements[0].Effect.ValueString() != "allow" || m.Name.ValueString() != "p" {
		t.Fatalf("diagnostics = %v, model = %+v", resp.Diagnostics, m)
	}
}

func TestReadTakesAChangeMadeOutsideTerraform(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := create(t, api, planWith(s, UnknownStr(), blocks(s, block(s, []string{"instances:Stop"}, "10.0.0.0/8")), UnknownStr())).State
	p := api.policies["pol_1"]
	p.Statements[0].Actions = []string{"instances:Delete"}
	api.policies["pol_1"] = p
	resp := &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	var actions []string
	getModel(t, resp.State).Statements[0].Actions.ElementsAs(Ctx, &actions, false)
	if len(actions) != 1 || actions[0] != "instances:Delete" {
		t.Fatalf("actions = %v", actions)
	}
	delete(api.policies, "pol_1")
	resp = &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Fatal("a deleted policy must leave state")
	}
}

func TestUpdateSendsIfMatchAndExplainsAConflict(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := create(t, api, planWith(s, UnknownStr(), blocks(s, block(s, []string{"instances:Stop"})), UnknownStr())).State
	plan := planWith(s, Str("pol_1"), blocks(s, block(s, []string{"instances:Stop", "instances:Start"})), UnknownStr())
	resp := &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: st}, resp)
	if resp.Diagnostics.HasError() || api.lastIfMatch != 1 || getModel(t, resp.State).Version.ValueInt64() != 2 {
		t.Fatalf("diagnostics = %v, If-Match = %d", resp.Diagnostics, api.lastIfMatch)
	}
	// The policy moved on elsewhere: the stale version is refused.
	p := api.policies["pol_1"]
	p.Version = 9
	api.policies["pol_1"] = p
	resp = &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: st}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "changed outside Terraform") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

// INVALID_POLICY field errors point at the statement block.
func TestInvalidPolicyFieldErrorsPointAtTheBlock(t *testing.T) {
	api := newFake()
	api.createErr = &client.APIError{Status: 422, Code: client.CodeInvalidPolicy, Errors: []client.FieldError{{Field: "statements[0].actions[1]", Code: "INVALID_ACTION", Message: "Unknown action"}}}
	s := testSchema(t)
	resp := create(t, api, planWith(s, UnknownStr(), blocks(s, block(s, []string{"instances:Stop"})), UnknownStr()))
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Detail(), "statements[0].actions[1]") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if p, ok := fieldPath("statements[2].conditions.source_ip[0]", true); !ok || p.String() != "statement[2].conditions.source_ip" {
		t.Fatalf("path = %s", p)
	}
	if p, ok := fieldPath("statements[0].actions[0]", false); !ok || p.String() != "policy_json" {
		t.Fatalf("path = %s", p)
	}
}

func validate(t *testing.T, stmts, policyJSON tftypes.Value) string {
	t.Helper()
	s := testSchema(t)
	cfg := tfsdk.Config{Schema: s, Raw: Object(s, map[string]tftypes.Value{"name": Str("p"), "statement": stmts, "policy_json": policyJSON})}
	resp := &resource.ValidateConfigResponse{}
	(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
	return ErrorText(resp.Diagnostics)
}

func TestValidateConfig(t *testing.T) {
	s := testSchema(t)
	null := tftypes.NewValue(tftypes.String, nil)
	tests := []struct {
		name       string
		stmts      tftypes.Value
		policyJSON tftypes.Value
		want       string
	}{
		{"blocks", blocks(s, block(s, []string{"instances:Stop"}, "10.0.0.0/8")), null, ""},
		{"json", blocks(s), Str(`{"statements":[{"effect":"allow","actions":["*"],"resources":["*"]}]}`), ""},
		{"neither", blocks(s), null, "exactly one"},
		{"both", blocks(s, block(s, []string{"*"})), Str(`{"statements":[]}`), "exactly one"},
		{"bad json", blocks(s), Str(`{"statements": [`), "Invalid policy JSON"},
		{"bad cidr in a block", blocks(s, block(s, []string{"*"}, "10.0.0.0/33")), null, "CIDR"},
		{"bad action in json", blocks(s), Str(`{"statements":[{"effect":"allow","actions":["Instances"],"resources":["*"]}]}`), "statements[0].actions"},
		{"empty json", blocks(s), Str(`{"statements":[]}`), "1 to 20 statements"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validate(t, tt.stmts, tt.policyJSON)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("errors = %q, want %q", got, tt.want)
			}
		})
	}
}

// ModifyPlan refuses an action the catalogue does not know, and shows the
// normal-form policy_json for blocks.
func TestModifyPlanChecksTheCatalogue(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	for actions, wantErr := range map[string]bool{"instances:Stop": false, "instances:*": false, "*:Read": false, "instances:Explode": true, "volumes:*": true} {
		plan := planWith(s, UnknownStr(), blocks(s, block(s, []string{actions})), UnknownStr())
		resp := &resource.ModifyPlanResponse{Plan: plan}
		(&Resource{api: api}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: plan, State: EmptyState(s)}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%s: diagnostics = %v", actions, resp.Diagnostics)
		}
		if !wantErr {
			var m model
			resp.Plan.Get(Ctx, &m)
			if !strings.Contains(m.PolicyJSON.ValueString(), actions) {
				t.Errorf("%s: planned policy_json = %q", actions, m.PolicyJSON.ValueString())
			}
		}
	}
}

func TestDeleteAndImport(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	st := create(t, api, planWith(s, UnknownStr(), blocks(s, block(s, []string{"*"})), UnknownStr())).State
	for range 2 { // gone the second time: still success
		resp := &resource.DeleteResponse{State: st}
		(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
	}
	for id, want := range map[string]string{"pol_1": "", "pol_pantech_developer": "managed policy", "team_1": "Invalid access policy id"} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if got := ErrorText(resp.Diagnostics); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: errors = %q", id, got)
		}
	}
}
