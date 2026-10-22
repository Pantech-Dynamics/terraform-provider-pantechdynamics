package webhook

import (
	"strings"
	"testing"

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

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func strList(vals ...string) tftypes.Value {
	elems := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, Str(v))
	}
	return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, elems)
}

func boolVal(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }

// planFor is the plan of a new webhook: the arguments known, every computed
// attribute unknown (the schema defaults already applied).
func planFor(s schema.Schema, url string, enabled bool, events ...string) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "url": Str(url), "description": Str(""), "events": strList(events...), "enabled": boolVal(enabled),
		"disabled_reason": UnknownStr(), "failing_since": UnknownStr(), "created_at": UnknownStr(), "updated_at": UnknownStr(), "secret": UnknownStr(),
	})
}

// stateFor is the stored state of webhook whk_1 as Create left it.
func stateFor(s schema.Schema, secret tftypes.Value) tfsdk.State {
	return State(s, map[string]tftypes.Value{
		"id": Str("whk_1"), "url": Str("https://example.com/hooks"), "description": Str(""), "events": strList("instance.*", "invoice.paid"),
		"enabled": boolVal(true), "disabled_reason": tftypes.NewValue(tftypes.String, nil), "failing_since": tftypes.NewValue(tftypes.String, nil),
		"created_at": Str("2026-10-22T10:00:00Z"), "updated_at": Str("2026-10-22T10:00:00Z"), "secret": secret,
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

func events(t *testing.T, m model) []string {
	t.Helper()
	var out []string
	if d := m.Events.ElementsAs(Ctx, &out, false); d.HasError() {
		t.Fatal(d)
	}
	return out
}

func create(t *testing.T, api *fakeAPI, evts ...string) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, "https://example.com/hooks", true, evts...)}, resp)
	return resp
}

// seeded returns a fake holding whk_1 as stateFor describes it.
func seeded() *fakeAPI {
	api := newFake()
	api.hooks["whk_1"] = client.Webhook{ID: "whk_1", URL: "https://example.com/hooks", Events: []string{"instance.*", "invoice.paid"}, Enabled: true, CreatedAt: at(0), UpdatedAt: at(0)}
	return api
}

func TestCreateSavesTheWebhookAndItsSecret(t *testing.T) {
	api := newFake()
	resp := create(t, api, "invoice.paid", "instance.*")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "whk_1" || m.Secret.ValueString() != "whsec_first" || !m.Enabled.ValueBool() || m.Description.ValueString() != "" {
		t.Errorf("model = %+v", m)
	}
	if got := events(t, m); strings.Join(got, ",") != "invoice.paid,instance.*" {
		t.Errorf("events = %v, want the configured order", got)
	}
	if strings.Join(api.lastCreate.Events, ",") != "invoice.paid,instance.*" || api.lastCreate.Description != "" {
		t.Errorf("create sent %+v", api.lastCreate)
	}
	if m.CreatedAt.ValueString() != "2026-10-22T10:00:00Z" || !m.DisabledReason.IsNull() || !m.FailingSince.IsNull() {
		t.Errorf("computed attributes = %+v", m)
	}
}

// A create retried on a gateway error is replayed without its secret. The
// webhook must still be saved, with a warning, so it is not orphaned.
func TestCreateWithoutSecretWarnsAndSavesTheWebhook(t *testing.T) {
	api := newFake()
	api.noSecret = true
	resp := create(t, api, "*")
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("diagnostics = %v, want one warning", resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "whk_1" || !m.Secret.IsNull() {
		t.Errorf("model = %+v, want the id saved and a null secret", m)
	}
}

func TestCreateRefusalsAreExplained(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"limit", &client.APIError{Status: 403, Code: client.CodeWebhookLimitReached}, "Delete one"},
		{"unavailable", &client.APIError{Status: 503, Code: client.CodeWebhooksUnavailable}, "not available"},
		{"field error", &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "url", Code: "INVALID_WEBHOOK_URL", Message: "not public"}}}, "INVALID_WEBHOOK_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFake()
			api.createErr = tt.err
			resp := create(t, api, "*")
			if !resp.Diagnostics.HasError() || !strings.Contains(ErrorText(resp.Diagnostics), tt.want) {
				t.Fatalf("diagnostics = %q, want %q", ErrorText(resp.Diagnostics), tt.want)
			}
			if !IsRemoved(resp.State) {
				t.Error("nothing was created, so no state should be saved")
			}
		})
	}
}

// The API never returns the secret again, so Read must keep it from state.
func TestReadKeepsTheSecretAndPicksUpChanges(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	h := api.hooks["whk_1"]
	h.Enabled = false
	reason := "failing"
	h.DisabledReason = &reason
	h.FailingSince = at(5)
	api.hooks["whk_1"] = h

	resp := &resource.ReadResponse{State: stateFor(s, Str("whsec_first"))}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s, Str("whsec_first"))}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.Secret.ValueString() != "whsec_first" {
		t.Errorf("secret = %v, want it kept from state", m.Secret)
	}
	if m.Enabled.ValueBool() || m.DisabledReason.ValueString() != "failing" || m.FailingSince.ValueString() != "2026-10-22T10:00:05Z" {
		t.Errorf("model = %+v", m)
	}
}

func TestReadRemovesADeletedWebhook(t *testing.T) {
	s := testSchema(t)
	resp := &resource.ReadResponse{State: stateFor(s, Str("whsec_first"))}
	(&Resource{api: newFake()}).Read(Ctx, resource.ReadRequest{State: stateFor(s, Str("whsec_first"))}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Errorf("want the webhook removed from state, diags %v", resp.Diagnostics)
	}
}

func TestReadReportsOtherErrors(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.getErr = &client.APIError{Status: 401, Code: "UNAUTHENTICATED", RequestID: "req_9"}
	resp := &resource.ReadResponse{State: stateFor(s, Str("whsec_first"))}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s, Str("whsec_first"))}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(ErrorText(resp.Diagnostics), "req_9") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func update(t *testing.T, api *fakeAPI, plan tfsdk.Plan, prior tfsdk.State) *resource.UpdateResponse {
	t.Helper()
	resp := &resource.UpdateResponse{State: prior}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: prior}, resp)
	return resp
}

// Only the arguments that changed are sent. In particular an unchanged
// enabled = true is not, because enabling clears the failure history.
func TestUpdateSendsOnlyChangedArguments(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	plan := Plan(s, map[string]tftypes.Value{
		"id": Str("whk_1"), "url": Str("https://example.com/v2"), "description": Str(""), "events": strList("volume.*", "instance.*"), "enabled": boolVal(true),
		"disabled_reason": UnknownStr(), "failing_since": UnknownStr(), "created_at": Str("2026-10-22T10:00:00Z"), "updated_at": UnknownStr(), "secret": Str("whsec_first"),
	})
	resp := update(t, api, plan, stateFor(s, Str("whsec_first")))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	req := api.lastUpdate
	if req.URL == nil || *req.URL != "https://example.com/v2" || req.Events == nil || strings.Join(*req.Events, ",") != "volume.*,instance.*" {
		t.Errorf("update sent %+v", req)
	}
	if req.Enabled != nil || req.Description != nil {
		t.Errorf("update sent unchanged fields: %+v", req)
	}
	m := getModel(t, resp.State)
	if m.URL.ValueString() != "https://example.com/v2" || m.Secret.ValueString() != "whsec_first" || m.UpdatedAt.ValueString() != "2026-10-22T10:00:30Z" {
		t.Errorf("model = %+v", m)
	}
	if got := events(t, m); strings.Join(got, ",") != "volume.*,instance.*" {
		t.Errorf("events = %v", got)
	}
}

func TestUpdateDisablesAndKeepsTheSecret(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	plan := Plan(s, map[string]tftypes.Value{
		"id": Str("whk_1"), "url": Str("https://example.com/hooks"), "description": Str("paused"), "events": strList("instance.*", "invoice.paid"), "enabled": boolVal(false),
		"disabled_reason": UnknownStr(), "failing_since": UnknownStr(), "created_at": Str("2026-10-22T10:00:00Z"), "updated_at": UnknownStr(), "secret": Str("whsec_first"),
	})
	resp := update(t, api, plan, stateFor(s, Str("whsec_first")))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	req := api.lastUpdate
	if req.Enabled == nil || *req.Enabled || req.Description == nil || *req.Description != "paused" || req.URL != nil || req.Events != nil {
		t.Errorf("update sent %+v", req)
	}
	m := getModel(t, resp.State)
	if m.Enabled.ValueBool() || m.DisabledReason.ValueString() != "disabled_by_user" || m.Secret.ValueString() != "whsec_first" {
		t.Errorf("model = %+v", m)
	}
}

func TestUpdateFieldErrorPointsAtTheAttribute(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.updateErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "events", Code: "INVALID_WEBHOOK_EVENTS", Message: "unknown event"}}}
	plan := Plan(s, map[string]tftypes.Value{
		"id": Str("whk_1"), "url": Str("https://example.com/hooks"), "description": Str(""), "events": strList("nope.happened"), "enabled": boolVal(true),
		"disabled_reason": UnknownStr(), "failing_since": UnknownStr(), "created_at": Str("2026-10-22T10:00:00Z"), "updated_at": UnknownStr(), "secret": Str("whsec_first"),
	})
	resp := update(t, api, plan, stateFor(s, Str("whsec_first")))
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Detail(), "INVALID_WEBHOOK_EVENTS") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if d, ok := errs[0].(interface{ Path() path.Path }); !ok || !d.Path().Equal(path.Root("events")) {
		t.Errorf("diagnostic is not on events: %v", errs[0])
	}
}

func TestDeleteTreatsGoneAsDone(t *testing.T) {
	s := testSchema(t)
	for name, api := range map[string]*fakeAPI{"present": seeded(), "already gone": newFake()} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.DeleteResponse{State: stateFor(s, Str("whsec_first"))}
			(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s, Str("whsec_first"))}, resp)
			if resp.Diagnostics.HasError() || api.deletes != 1 || len(api.hooks) != 0 {
				t.Errorf("diags %v, deletes %d, left %d", resp.Diagnostics, api.deletes, len(api.hooks))
			}
		})
	}
}

func TestDeleteReportsOtherErrors(t *testing.T) {
	s := testSchema(t)
	api := seeded()
	api.deleteErr = &client.APIError{Status: 403, Code: "FORBIDDEN"}
	resp := &resource.DeleteResponse{State: stateFor(s, Str("whsec_first"))}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s, Str("whsec_first"))}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
}

// After an import the secret is null: the API cannot return it.
func TestImportThenReadLeavesTheSecretNull(t *testing.T) {
	s := testSchema(t)
	r := &Resource{api: seeded()}

	imp := &resource.ImportStateResponse{State: EmptyState(s)}
	r.ImportState(Ctx, resource.ImportStateRequest{ID: "whk_1"}, imp)
	if imp.Diagnostics.HasError() {
		t.Fatal(imp.Diagnostics)
	}
	read := &resource.ReadResponse{State: imp.State}
	r.Read(Ctx, resource.ReadRequest{State: imp.State}, read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	m := getModel(t, read.State)
	if m.URL.ValueString() != "https://example.com/hooks" || !m.Secret.IsNull() || len(events(t, m)) != 2 {
		t.Errorf("model = %+v", m)
	}
}

func TestImportRejectsAWrongID(t *testing.T) {
	s := testSchema(t)
	resp := &resource.ImportStateResponse{State: EmptyState(s)}
	(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: "sshk_1"}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(ErrorText(resp.Diagnostics), `"whk_"`) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestSecretIsSensitiveAndKeptAcrossPlans(t *testing.T) {
	a, ok := testSchema(t).Attributes["secret"].(schema.StringAttribute)
	if !ok || !a.Sensitive || !a.Computed || len(a.PlanModifiers) != 1 {
		t.Fatalf("secret = %+v", a)
	}
}

func TestURLValidator(t *testing.T) {
	tests := map[string]bool{
		"https://example.com/hooks": true,
		"HTTPS://example.com":       true,
		"http://example.com/hooks":  false,
		"https://":                  false,
		"example.com":               false,
		"https://example.com/" + strings.Repeat("a", 2100): false,
	}
	for url, valid := range tests {
		resp := &validator.StringResponse{}
		httpsURL{}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("url"), ConfigValue: types.StringValue(url)}, resp)
		if resp.Diagnostics.HasError() == valid {
			t.Errorf("%.40s: valid = %v, diags %v", url, !resp.Diagnostics.HasError(), resp.Diagnostics)
		}
	}
}

func TestDescriptionValidator(t *testing.T) {
	for desc, valid := range map[string]bool{"": true, strings.Repeat("é", 255): true, strings.Repeat("a", 256): false} {
		resp := &validator.StringResponse{}
		maxLength{max: maxDescriptionLength}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("description"), ConfigValue: types.StringValue(desc)}, resp)
		if resp.Diagnostics.HasError() == valid {
			t.Errorf("len %d: valid = %v", len(desc), !resp.Diagnostics.HasError())
		}
	}
}

func TestEventListValidator(t *testing.T) {
	many := make([]string, 101)
	for i := range many {
		many[i] = "e" + strings.Repeat("x", i)
	}
	tests := []struct {
		name   string
		events []string
		valid  bool
	}{
		{"exact, wildcard and all", []string{"instance.created", "volume.*", "*"}, true},
		{"empty list", []string{}, false},
		{"empty entry", []string{"instance.*", " "}, false},
		{"duplicate", []string{"instance.*", "invoice.paid", "instance.*"}, false},
		{"too many", many, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, d := types.ListValueFrom(Ctx, types.StringType, tt.events)
			if d.HasError() {
				t.Fatal(d)
			}
			resp := &validator.ListResponse{}
			eventList{}.ValidateList(Ctx, validator.ListRequest{Path: path.Root("events"), ConfigValue: list}, resp)
			if resp.Diagnostics.HasError() == tt.valid {
				t.Errorf("valid = %v, diags %v", !resp.Diagnostics.HasError(), resp.Diagnostics)
			}
		})
	}
}
