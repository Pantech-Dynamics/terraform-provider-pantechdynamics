package instance

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestToCreateRequestCarriesSubnetAndNetwork(t *testing.T) {
	req, diags := toCreateRequest(ctx, model{
		Name: types.StringValue("web"), PlanSlug: types.StringValue("individual"), ImageSlug: types.StringValue("ubuntu-24-04"),
		SubnetID: types.StringValue("snet_1"), NetworkID: types.StringUnknown(), SecurityGroupID: types.StringUnknown(),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if req.SubnetID != "snet_1" || req.NetworkID != "" || req.SecurityGroupID != "" {
		t.Errorf("request = %+v, want only subnet_id set", req)
	}
}

func TestFromAPIResponseMapsVPCPlacement(t *testing.T) {
	inst := running("vm_1", "web")
	subnet, network := "snet_1", "net_1"
	inst.SubnetID, inst.NetworkID = &subnet, &network

	m, diags := fromAPIResponse(ctx, model{}, &inst)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if m.SubnetID.ValueString() != "snet_1" || m.NetworkID.ValueString() != "net_1" {
		t.Errorf("subnet %v, network %v", m.SubnetID, m.NetworkID)
	}

	std := running("vm_2", "std") // a standard instance reports neither
	m, _ = fromAPIResponse(ctx, model{}, &std)
	if !m.SubnetID.IsNull() || !m.NetworkID.IsNull() {
		t.Errorf("standard instance: subnet %v, network %v, want null", m.SubnetID, m.NetworkID)
	}
}

func TestPendingModelNeverHoldsUnknownPlacement(t *testing.T) {
	m := pendingModel(model{SubnetID: types.StringValue("snet_1"), NetworkID: types.StringUnknown()}, "vm_1")
	if m.SubnetID.ValueString() != "snet_1" || !m.NetworkID.IsNull() {
		t.Errorf("subnet %v, network %v", m.SubnetID, m.NetworkID)
	}
}

func TestValidateConfigRejectsASecurityGroupInAVPC(t *testing.T) {
	s := testSchema(t)
	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	tests := []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
	}{
		{"standard with a group", map[string]tftypes.Value{"security_group_id": str("sg_1")}, false},
		{"vpc without a group", map[string]tftypes.Value{"subnet_id": str("snet_1")}, false},
		{"vpc with a group", map[string]tftypes.Value{"subnet_id": str("snet_1"), "security_group_id": str("sg_1")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: values(s, tt.set)}}, resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("error = %v, want %v", resp.Diagnostics.HasError(), tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(errorText(resp.Diagnostics), "not allowed in a VPC") {
				t.Errorf("diagnostics = %q", errorText(resp.Diagnostics))
			}
		})
	}
}
