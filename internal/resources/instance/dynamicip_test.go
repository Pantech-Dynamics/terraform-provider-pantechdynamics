package instance

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func dynamicIPTFValue(schedule, connections string) tftypes.Value {
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"schedule":    tftypes.String,
		"connections": tftypes.String,
	}}
	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"schedule":    str(schedule),
		"connections": str(connections),
	})
}

func TestDynamicIPOnCreateAndRead(t *testing.T) {
	s := testSchema(t)

	t.Run("create sends address_rotation and preserves dynamic_ip in state", func(t *testing.T) {
		api := seeded()
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{
			Plan: planFor(s, "", "web", map[string]tftypes.Value{
				"ssh_key_id":        str("sshk_1"),
				"security_group_id": str("sg_narrow"),
				"dynamic_ip":        dynamicIPTFValue(client.DynamicIPScheduleDaily, client.DynamicIPConnectionsMove),
			}),
		}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.lastCreate.AddressRotation == nil || api.lastCreate.AddressRotation.Schedule != "daily" || api.lastCreate.AddressRotation.Connections != "move" {
			t.Fatalf("AddressRotation = %+v", api.lastCreate.AddressRotation)
		}
		m := getModel(t, resp.State)
		if m.DynamicIP.IsNull() || m.DynamicIP.IsUnknown() {
			t.Fatalf("dynamic_ip must be preserved in state, got %v", m.DynamicIP)
		}

		// Read must preserve dynamic_ip because GET /instances/{id} does not return it.
		rresp := resource.ReadResponse{State: resp.State}
		newTestResource(api).Read(ctx, resource.ReadRequest{State: resp.State}, &rresp)
		if rresp.Diagnostics.HasError() {
			t.Fatal(errorText(rresp.Diagnostics))
		}
		rm := getModel(t, rresp.State)
		if !rm.DynamicIP.Equal(m.DynamicIP) {
			t.Fatalf("read dynamic_ip = %v, want %v", rm.DynamicIP, m.DynamicIP)
		}
	})

	t.Run("fromAPIResponse preserves configured dynamic_ip", func(t *testing.T) {
		want, diags := types.ObjectValue(dynamicIPAttrTypes, map[string]attr.Value{
			"schedule":    types.StringValue("weekly"),
			"connections": types.StringValue("keep"),
		})
		if diags.HasError() {
			t.Fatal(diags)
		}
		inst := running("vm_1", "web")
		m, diags := fromAPIResponse(ctx, model{DynamicIP: want}, &inst)
		if diags.HasError() {
			t.Fatal(diags)
		}
		if !m.DynamicIP.Equal(want) {
			t.Fatalf("DynamicIP = %v, want %v", m.DynamicIP, want)
		}
	})

	t.Run("create refusal for dynamic_ip code gives hint on dynamic_ip", func(t *testing.T) {
		api := seeded()
		api.createErr = &client.APIError{Status: 409, Code: client.CodeAddressRotationNeedsSecurityGroup, Detail: "group allows private network", RequestID: "req_dip"}
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{
			Plan: planFor(s, "", "web", map[string]tftypes.Value{
				"ssh_key_id":        str("sshk_1"),
				"security_group_id": str("sg_open"),
				"dynamic_ip":        dynamicIPTFValue(client.DynamicIPScheduleDaily, client.DynamicIPConnectionsKeep),
			}),
		}, &resp)
		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "Narrow the ingress rules") || !strings.Contains(text, "req_dip") {
			t.Fatalf("diags = %s", text)
		}
		if !attachedTo(resp.Diagnostics, path.Root("dynamic_ip")) {
			t.Fatal("error must point at dynamic_ip")
		}
	})
}

func TestValidateConfigDynamicIP(t *testing.T) {
	s := testSchema(t)
	dip := dynamicIPTFValue(client.DynamicIPScheduleDaily, client.DynamicIPConnectionsKeep)
	for _, tt := range []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
		wantSub string
	}{
		{"valid with security_group_id", map[string]tftypes.Value{"security_group_id": str("sg_1"), "dynamic_ip": dip}, false, ""},
		{"valid with unknown security_group_id reference", map[string]tftypes.Value{"security_group_id": unknown(), "dynamic_ip": dip}, false, ""},
		{"missing security_group_id", map[string]tftypes.Value{"dynamic_ip": dip}, true, "requires security_group_id"},
		{"empty security_group_id", map[string]tftypes.Value{"security_group_id": str(""), "dynamic_ip": dip}, true, "requires security_group_id"},
		{"with subnet_id", map[string]tftypes.Value{"subnet_id": str("snet_1"), "security_group_id": str("sg_1"), "dynamic_ip": dip}, true, "not allowed in a VPC"},
		{"with network_id", map[string]tftypes.Value{"network_id": str("net_1"), "security_group_id": str("sg_1"), "dynamic_ip": dip}, true, "not allowed in a VPC"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var resp resource.ValidateConfigResponse
			(&Resource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: values(s, tt.set)}}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s, wantErr = %v", errorText(resp.Diagnostics), tt.wantErr)
			}
			if tt.wantSub != "" && !strings.Contains(errorText(resp.Diagnostics), tt.wantSub) {
				t.Fatalf("diags = %s, want %q", errorText(resp.Diagnostics), tt.wantSub)
			}
		})
	}
}
