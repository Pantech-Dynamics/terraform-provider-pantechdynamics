package securitygroup

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

func TestRulesRoundTrip(t *testing.T) {
	api := []client.SecurityGroupRule{
		{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/8"},
		{Direction: "ingress", Protocol: "icmp", PortRange: "", CIDR: "0.0.0.0/0"},
	}

	set, diags := rulesToSet(ctx, api)
	if diags.HasError() {
		t.Fatal(diags)
	}
	back, diags := rulesFromSet(ctx, set)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !sameRules(api, back) {
		t.Fatalf("round trip changed the rules: %+v", back)
	}

	var models []ruleModel
	set.ElementsAs(ctx, &models, false)
	for _, m := range models {
		if m.Protocol.ValueString() == "icmp" && !m.PortRange.IsNull() {
			t.Fatalf("an empty port_range from the API must be null in state, got %v", m.PortRange)
		}
	}
}

func TestSameRulesIgnoresOrder(t *testing.T) {
	a := client.SecurityGroupRule{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/8"}
	b := client.SecurityGroupRule{Direction: "egress", Protocol: "all", CIDR: "0.0.0.0/0"}

	tests := []struct {
		name string
		x, y []client.SecurityGroupRule
		want bool
	}{
		{"same order", []client.SecurityGroupRule{a, b}, []client.SecurityGroupRule{a, b}, true},
		{"different order", []client.SecurityGroupRule{a, b}, []client.SecurityGroupRule{b, a}, true},
		{"different count", []client.SecurityGroupRule{a}, []client.SecurityGroupRule{a, b}, false},
		{"different rule", []client.SecurityGroupRule{a}, []client.SecurityGroupRule{b}, false},
		{"port differs", []client.SecurityGroupRule{a}, []client.SecurityGroupRule{{Direction: "ingress", Protocol: "tcp", PortRange: "23", CIDR: "10.0.0.0/8"}}, false},
		{"both empty", nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameRules(tt.x, tt.y); got != tt.want {
				t.Fatalf("sameRules = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFromAPIResponse(t *testing.T) {
	created := time.Date(2026, 10, 3, 22, 57, 3, 517994000, time.UTC)
	sg := &client.SecurityGroup{
		ID: "sg_1", Name: "web", ObservedState: "active", CreatedAt: &created,
		Rules: []client.SecurityGroupRule{{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/8"}},
	}

	m, diags := fromAPIResponse(ctx, model{}, sg)

	if diags.HasError() {
		t.Fatal(diags)
	}
	if m.ID.ValueString() != "sg_1" || m.Name.ValueString() != "web" || m.ObservedState.ValueString() != "active" {
		t.Fatalf("m = %+v", m)
	}
	if m.CreatedAt.ValueString() != "2026-10-03T22:57:03Z" || !m.UpdatedAt.IsNull() {
		t.Fatalf("created_at = %v, updated_at = %v: want whole seconds, and null when the API sends none", m.CreatedAt, m.UpdatedAt)
	}
	if len(m.Rules.Elements()) != 1 {
		t.Fatalf("rules = %v", m.Rules)
	}
}

func TestPendingModelHasNoUnknownValues(t *testing.T) {
	rules, _ := rulesToSet(ctx, nil)
	plan := model{
		ID: types.StringUnknown(), Name: types.StringValue("web"), Rules: rules,
		ObservedState: types.StringUnknown(), CreatedAt: types.StringUnknown(), UpdatedAt: types.StringUnknown(),
	}

	m := pendingModel(plan, "sg_9")

	if m.ID.ValueString() != "sg_9" || m.ID.IsUnknown() || m.ObservedState.IsUnknown() || m.CreatedAt.IsUnknown() || m.UpdatedAt.IsUnknown() {
		t.Fatalf("pending state must be fully known so it can be saved: %+v", m)
	}
}
