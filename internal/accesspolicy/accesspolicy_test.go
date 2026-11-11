package accesspolicy

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func ptr(b bool) *bool { return &b }

func stmt(effect string, actions, resources []string) client.PolicyStatement {
	return client.PolicyStatement{Effect: effect, Actions: actions, Resources: resources}
}

func TestValidate(t *testing.T) {
	many := make([]client.PolicyStatement, MaxStatements+1)
	for i := range many {
		many[i] = stmt("allow", []string{"*"}, []string{"*"})
	}
	tests := []struct {
		name       string
		statements []client.PolicyStatement
		wantField  string
		wantMsg    string
	}{
		{"valid", []client.PolicyStatement{stmt("allow", []string{"instances:Stop", "*:Read", "instances:*", "*"}, []string{"*", "vm_01JD", "name:staging-*"})}, "", ""},
		{"no statements", nil, "", "1 to 20 statements"},
		{"too many", many, "", "1 to 20 statements"},
		{"bad effect", []client.PolicyStatement{stmt("Allow", []string{"*"}, []string{"*"})}, "effect", `"allow" or "deny"`},
		{"no actions", []client.PolicyStatement{stmt("allow", nil, []string{"*"})}, "actions", "1 to 50 actions"},
		{"bad action form", []client.PolicyStatement{stmt("allow", []string{"instances"}, []string{"*"})}, "actions", "<service>:<verb>"},
		{"bad service", []client.PolicyStatement{stmt("allow", []string{"Instances:Stop"}, []string{"*"})}, "actions", "the service"},
		{"bad verb", []client.PolicyStatement{stmt("allow", []string{"instances:stop-now"}, []string{"*"})}, "actions", "the verb"},
		{"bad resource", []client.PolicyStatement{stmt("allow", []string{"*"}, []string{"prod-api"})}, "resources", "name:<pattern>"},
		{"empty name pattern", []client.PolicyStatement{stmt("allow", []string{"*"}, []string{"name:"})}, "resources", "1 to 253"},
		{"bad cidr", []client.PolicyStatement{{Effect: "allow", Actions: []string{"*"}, Resources: []string{"*"}, Conditions: &client.PolicyConditions{SourceIP: []string{"10.0.0.0/33"}}}}, "conditions.source_ip", "CIDR"},
		{"empty source_ip", []client.PolicyStatement{{Effect: "allow", Actions: []string{"*"}, Resources: []string{"*"}, Conditions: &client.PolicyConditions{SourceIP: []string{}}}}, "conditions.source_ip", "1 to 20"},
		{"duplicate sid", []client.PolicyStatement{{Sid: "A", Effect: "allow", Actions: []string{"*"}, Resources: []string{"*"}}, {Sid: "A", Effect: "deny", Actions: []string{"*"}, Resources: []string{"*"}}}, "sid", "unique"},
		{"bad sid", []client.PolicyStatement{{Sid: "no spaces", Effect: "allow", Actions: []string{"*"}, Resources: []string{"*"}}}, "sid", "letters, digits"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := Validate(tt.statements)
			if tt.wantMsg == "" {
				if len(problems) != 0 {
					t.Fatalf("problems = %+v", problems)
				}
				return
			}
			if len(problems) == 0 || problems[len(problems)-1].Field != tt.wantField || !strings.Contains(problems[len(problems)-1].Message, tt.wantMsg) {
				t.Fatalf("problems = %+v, want %s: %s", problems, tt.wantField, tt.wantMsg)
			}
		})
	}
}

func TestCanonicalCIDR(t *testing.T) {
	for in, want := range map[string]string{
		"102.89.1.2":     "102.89.1.2/32",
		"102.89.1.2/16":  "102.89.0.0/16",
		"2001:db8::1/48": "2001:db8::/48",
		"2001:db8::1":    "2001:db8::1/128",
		"::ffff:1.2.3.4": "",
		"10.0.0.0/33":    "",
		"nope":           "",
	} {
		got, ok := CanonicalCIDR(in)
		if got != want || ok != (want != "") {
			t.Errorf("CanonicalCIDR(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestEqualIgnoresOrderDuplicatesAndCIDRSpelling(t *testing.T) {
	a := []client.PolicyStatement{{Sid: "S", Effect: "allow", Actions: []string{"instances:Stop", "instances:Start", "instances:Stop"}, Resources: []string{"*"},
		Conditions: &client.PolicyConditions{SourceIP: []string{"102.89.1.2/16", "10.0.0.1"}}}}
	b := []client.PolicyStatement{{Sid: "S", Effect: "allow", Actions: []string{"instances:Start", "instances:Stop"}, Resources: []string{"*"},
		Conditions: &client.PolicyConditions{SourceIP: []string{"10.0.0.1/32", "102.89.0.0/16"}}}}
	if !Equal(a, b) {
		t.Fatal("equivalent statements differ")
	}
	// mfa false and no mfa are different on a deny, so they never compare equal.
	c := []client.PolicyStatement{{Effect: "deny", Actions: []string{"*"}, Resources: []string{"*"}, Conditions: &client.PolicyConditions{MFA: ptr(false)}}}
	d := []client.PolicyStatement{{Effect: "deny", Actions: []string{"*"}, Resources: []string{"*"}}}
	if Equal(c, d) {
		t.Fatal("mfa false and no mfa must differ")
	}
	// Empty conditions are no conditions.
	e := []client.PolicyStatement{{Effect: "deny", Actions: []string{"*"}, Resources: []string{"*"}, Conditions: &client.PolicyConditions{}}}
	if !Equal(d, e) {
		t.Fatal("empty conditions must equal none")
	}
}

func TestDocumentRoundTrip(t *testing.T) {
	statements := []client.PolicyStatement{{Sid: "S", Effect: "deny", Actions: []string{"instances:Delete"}, Resources: []string{"name:prod-*"}, Conditions: &client.PolicyConditions{MFA: ptr(false)}}}
	doc, err := MarshalDocument(statements)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, `"name:prod-*"`) || !strings.HasPrefix(doc, "{\n  \"statements\"") {
		t.Fatalf("doc = %s", doc)
	}
	back, err := ParseDocument(doc)
	if err != nil || !Equal(back, statements) {
		t.Fatalf("back = %+v, err = %v", back, err)
	}
	if _, err := ParseDocument(`{"statements":[{"effect":"allow","action":["*"],"resources":["*"]}]}`); err == nil {
		t.Fatal("an unknown field must be refused")
	}
	if _, err := ParseDocument(`{"statements":[]} {}`); err == nil {
		t.Fatal("two documents must be refused")
	}
}

func TestMatchesAction(t *testing.T) {
	for _, tt := range []struct {
		pattern, action string
		want            bool
	}{
		{"*", "instances:Stop", true},
		{"instances:*", "instances:Stop", true},
		{"*:Read", "dns:Read", true},
		{"*:Read", "dns:ReadContact", false},
		{"instances:St*", "instances:Stop", true},
		{"instances:Stop", "instances:Start", false},
		{"inst*:Stop", "instances:Stop", true},
	} {
		if got := MatchesAction(tt.pattern, tt.action); got != tt.want {
			t.Errorf("MatchesAction(%q, %q) = %v", tt.pattern, tt.action, got)
		}
	}
}

func TestNameProblem(t *testing.T) {
	for name, ok := range map[string]bool{"staging-operators": true, "CI key 1": true, "pantech_x": false, "-x": false, " x": false, "a/b": false, strings.Repeat("a", 65): false} {
		if (NameProblem(name) == "") != ok {
			t.Errorf("%q: problem = %q", name, NameProblem(name))
		}
	}
}

func TestToAPIAndFromAPIRoundTrip(t *testing.T) {
	ctx := context.Background()
	in := []client.PolicyStatement{
		{Sid: "A", Effect: "allow", Actions: []string{"instances:Start", "instances:Stop"}, Resources: []string{"name:staging-*"}, Conditions: &client.PolicyConditions{SourceIP: []string{"102.89.0.0/16"}, MFA: ptr(true)}},
		{Effect: "deny", Actions: []string{"*"}, Resources: []string{"*"}},
	}
	models, diags := FromAPI(ctx, in)
	if diags.HasError() || !models[1].Sid.IsNull() || models[1].Conditions != nil || models[0].Conditions.MFA.ValueBool() != true {
		t.Fatalf("models = %+v, diags = %v", models, diags)
	}
	if !Known(models) {
		t.Fatal("known models reported unknown")
	}
	out, diags := ToAPI(ctx, models)
	if diags.HasError() || !Equal(in, out) {
		t.Fatalf("out = %+v", out)
	}
}

func TestProblemPath(t *testing.T) {
	if p := ProblemPath("statement", Problem{Statement: 1, Field: "conditions.source_ip"}); !p.Equal(path.Root("statement").AtListIndex(1).AtName("conditions").AtName("source_ip")) {
		t.Errorf("path = %s", p)
	}
	if p := ProblemPath("statement", Problem{Statement: -1}); !p.Equal(path.Root("statement")) {
		t.Errorf("path = %s", p)
	}
}
