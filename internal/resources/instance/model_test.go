package instance

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

func TestToCreateRequest(t *testing.T) {
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "dev"})

	tests := []struct {
		name string
		in   model
		want client.CreateInstanceRequest
	}{
		{
			"everything set",
			model{Name: types.StringValue("web"), PlanSlug: types.StringValue("individual"), ImageSlug: types.StringValue("ubuntu-24-04"),
				SSHKeyID: types.StringValue("sshk_1"), Region: types.StringValue("af-abj"), SecurityGroupID: types.StringValue("sg_1"), Tags: tags},
			client.CreateInstanceRequest{Name: "web", PlanSlug: "individual", ImageSlug: "ubuntu-24-04", SSHKeyID: "sshk_1", Region: "af-abj", SecurityGroupID: "sg_1", Tags: map[string]string{"env": "dev"}},
		},
		{
			"unset optionals are omitted",
			model{Name: types.StringValue("web"), PlanSlug: types.StringValue("individual"), ImageSlug: types.StringValue("ubuntu-24-04"),
				SSHKeyID: types.StringNull(), Region: types.StringUnknown(), SecurityGroupID: types.StringUnknown(), Tags: types.MapUnknown(types.StringType)},
			client.CreateInstanceRequest{Name: "web", PlanSlug: "individual", ImageSlug: "ubuntu-24-04"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, diags := toCreateRequest(ctx, tt.in)
			if diags.HasError() {
				t.Fatal(diags)
			}
			if got.Name != tt.want.Name || got.PlanSlug != tt.want.PlanSlug || got.ImageSlug != tt.want.ImageSlug ||
				got.SSHKeyID != tt.want.SSHKeyID || got.Region != tt.want.Region || got.SecurityGroupID != tt.want.SecurityGroupID ||
				len(got.Tags) != len(tt.want.Tags) || got.Tags["env"] != tt.want.Tags["env"] {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFromAPIResponse(t *testing.T) {
	inst := running("vm_1", "web")
	inst.Tags = nil // the API may send null

	m, diags := fromAPIResponse(ctx, model{SSHKeyID: types.StringValue("sshk_1")}, &inst)

	if diags.HasError() {
		t.Fatal(diags)
	}
	if m.ID.ValueString() != "vm_1" || m.Zone.ValueString() != "af-abj-1" || m.ObservedState.ValueString() != "running" || m.SecurityGroupID.ValueString() != "sg_default" {
		t.Fatalf("m = %+v", m)
	}
	if !m.PublicIPv4.IsNull() || m.PrivateIPv4.ValueString() != "102.211.122.77" {
		t.Fatalf("public = %v, private = %v: a missing public ip must be null", m.PublicIPv4, m.PrivateIPv4)
	}
	if m.SSHKeyID.ValueString() != "sshk_1" {
		t.Fatalf("ssh_key_id = %v: the API never returns it, so it must be carried over", m.SSHKeyID)
	}
	if m.Tags.IsNull() || m.Tags.IsUnknown() || len(m.Tags.Elements()) != 0 {
		t.Fatalf("tags = %v, want a known empty map, not null", m.Tags)
	}
	if m.CreatedAt.ValueString() != "2026-10-03T23:38:52Z" {
		t.Fatalf("created_at = %v, want whole seconds", m.CreatedAt)
	}
}

func TestFromAPIResponseUnknownSSHKeyBecomesNull(t *testing.T) {
	inst := running("vm_1", "web")
	m, _ := fromAPIResponse(ctx, model{SSHKeyID: types.StringUnknown()}, &inst)
	if !m.SSHKeyID.IsNull() {
		t.Fatalf("ssh_key_id = %v: state may not hold an unknown value", m.SSHKeyID)
	}
}

func TestPendingModelHasNoUnknownValues(t *testing.T) {
	plan := model{
		ID: types.StringUnknown(), Name: types.StringValue("web"), PlanSlug: types.StringValue("individual"), ImageSlug: types.StringValue("ubuntu-24-04"),
		SSHKeyID: types.StringValue("sshk_1"), Region: types.StringUnknown(), SecurityGroupID: types.StringUnknown(), Tags: types.MapUnknown(types.StringType),
		ObservedState: types.StringUnknown(), Zone: types.StringUnknown(), PublicIPv4: types.StringUnknown(), PrivateIPv4: types.StringUnknown(),
		CreatedAt: types.StringUnknown(), UpdatedAt: types.StringUnknown(),
	}

	m := pendingModel(plan, "vm_9")

	unknowns := []struct {
		name string
		v    interface{ IsUnknown() bool }
	}{
		{"id", m.ID}, {"region", m.Region}, {"security_group_id", m.SecurityGroupID}, {"tags", m.Tags}, {"observed_state", m.ObservedState},
		{"zone", m.Zone}, {"public_ipv4", m.PublicIPv4}, {"private_ipv4", m.PrivateIPv4}, {"created_at", m.CreatedAt}, {"updated_at", m.UpdatedAt},
	}
	for _, u := range unknowns {
		if u.v.IsUnknown() {
			t.Errorf("%s is unknown in the pending state, so it could not be saved", u.name)
		}
	}
	if m.ID.ValueString() != "vm_9" || m.SSHKeyID.ValueString() != "sshk_1" {
		t.Fatalf("m = %+v", m)
	}
}
