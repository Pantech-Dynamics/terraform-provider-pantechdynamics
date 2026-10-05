package instance

import (
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func boolean(v bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, v) }

// attached is a running instance with the private network interface.
func attached() client.Instance {
	inst := instanceWith("running", "individual")
	inst.PrivateNetworkState, inst.PrivateNetworkIP = client.PrivateNetworkAttached, ptr("10.250.0.7")
	return inst
}

var refusedGroup = &client.APIError{Status: 409, Code: client.CodeSecurityGroupAllowsPrivateNetwork, RequestID: "req_1",
	Detail: "Security group sg_default allows 0.0.0.0/0 on icmp, which includes the private network 10.250.0.0/20. Narrow that rule."}

func TestPrivateNetworkOnCreate(t *testing.T) {
	s := testSchema(t)
	create := func(api *fakeAPI, extra map[string]tftypes.Value) resource.CreateResponse {
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "", "web", extra)}, &resp)
		return resp
	}

	t.Run("attaches once running and stores the address", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"private_network": boolean(true), "private_network_ip": unknown()})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if api.attaches != 1 || !m.PrivateNetwork.ValueBool() || m.PrivateNetIP.ValueString() != "10.250.0.7" {
			t.Fatalf("attaches = %d, model = %+v", api.attaches, m)
		}
	})

	t.Run("unset sends nothing and records no interface", func(t *testing.T) {
		api := seeded()
		resp := create(api, map[string]tftypes.Value{"private_network": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue), "private_network_ip": unknown()})
		if resp.Diagnostics.HasError() || api.attaches != 0 {
			t.Fatalf("diags = %s, attaches = %d", errorText(resp.Diagnostics), api.attaches)
		}
		if m := getModel(t, resp.State); m.PrivateNetwork.ValueBool() || !m.PrivateNetIP.IsNull() {
			t.Fatalf("model = %+v", m)
		}
	})

	t.Run("a refused group shows the API's message and keeps the instance", func(t *testing.T) {
		api := seeded()
		api.attachErr = refusedGroup
		resp := create(api, map[string]tftypes.Value{"private_network": boolean(true), "private_network_ip": unknown()})
		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "includes the private network 10.250.0.0/20. Narrow that rule.") || !strings.Contains(text, "req_1") || !strings.Contains(text, "Narrow the ingress rules") {
			t.Fatalf("diags = %s", text)
		}
		if !attachedTo(resp.Diagnostics, path.Root("private_network")) {
			t.Fatal("the error must point at private_network")
		}
		if m := getModel(t, resp.State); m.ID.ValueString() != "vm_1" || m.PrivateNetwork.ValueBool() {
			t.Fatalf("state = %+v: the paid instance must stay in state, without the interface", m)
		}
	})
}

func TestPrivateNetworkInPlace(t *testing.T) {
	t.Run("attach on an existing instance, nothing else touched", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		resp := updateFrom(t, api, "running", map[string]tftypes.Value{"private_network": boolean(false)},
			map[string]tftypes.Value{"private_network": boolean(true), "private_network_ip": unknown()})
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.attaches != 1 || api.stops != 0 || api.starts != 0 || api.creates != 0 || api.deletes != 0 {
			t.Fatalf("attaches = %d, stops = %d, starts = %d", api.attaches, api.stops, api.starts)
		}
		if got := getModel(t, resp.State).PrivateNetIP.ValueString(); got != "10.250.0.7" {
			t.Fatalf("private_network_ip = %q", got)
		}
	})

	t.Run("detach clears the address", func(t *testing.T) {
		api := seeded(attached())
		resp := updateFrom(t, api, "running", map[string]tftypes.Value{"private_network": boolean(true), "private_network_ip": str("10.250.0.7")},
			map[string]tftypes.Value{"private_network": boolean(false), "private_network_ip": unknown()})
		if resp.Diagnostics.HasError() || api.detaches != 1 {
			t.Fatalf("diags = %s, detaches = %d", errorText(resp.Diagnostics), api.detaches)
		}
		if m := getModel(t, resp.State); m.PrivateNetwork.ValueBool() || !m.PrivateNetIP.IsNull() {
			t.Fatalf("model = %+v", m)
		}
	})

	t.Run("detach goes before a group change, attach after one", func(t *testing.T) {
		api := seeded(attached())
		updateFrom(t, api, "running", map[string]tftypes.Value{"private_network": boolean(true)},
			map[string]tftypes.Value{"private_network": boolean(false), "security_group_id": str("sg_web")})
		if !slices.Equal(api.netOrder, []string{"detach", "group"}) {
			t.Fatalf("order = %v", api.netOrder)
		}

		api = seeded(instanceWith("running", "individual"))
		updateFrom(t, api, "running", map[string]tftypes.Value{"private_network": boolean(false)},
			map[string]tftypes.Value{"private_network": boolean(true), "security_group_id": str("sg_narrow")})
		if !slices.Equal(api.netOrder, []string{"group", "attach"}) {
			t.Fatalf("order = %v", api.netOrder)
		}
	})

	t.Run("unset leaves the interface alone", func(t *testing.T) {
		api := seeded(attached())
		resp := updateFrom(t, api, "running", map[string]tftypes.Value{"private_network": boolean(true)}, nil)
		if resp.Diagnostics.HasError() || api.attaches+api.detaches != 0 {
			t.Fatalf("diags = %s, attaches = %d, detaches = %d", errorText(resp.Diagnostics), api.attaches, api.detaches)
		}
	})

	t.Run("a group change refused while attached shows the API's message on security_group_id", func(t *testing.T) {
		api := seeded(attached())
		api.groupErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Detail: "The request is invalid.",
			Errors: []client.FieldError{{Field: "security_group_id", Code: client.CodeSecurityGroupAllowsPrivateNetwork, Message: "group sg_open allows 0.0.0.0/0, which includes the private network"}}}
		resp := updateFrom(t, api, "running", map[string]tftypes.Value{"private_network": boolean(true)},
			map[string]tftypes.Value{"security_group_id": str("sg_open")})
		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "which includes the private network") || !strings.Contains(text, "Narrow the ingress rules") {
			t.Fatalf("diags = %s", text)
		}
		if !attachedTo(resp.Diagnostics, path.Root("security_group_id")) {
			t.Fatal("the error must point at security_group_id")
		}
	})
}

func TestReadMapsThePrivateNetwork(t *testing.T) {
	for _, tt := range []struct {
		state  string
		ip     *string
		wantOn bool
	}{
		{client.PrivateNetworkNone, nil, false},
		{"", nil, false},
		{client.PrivateNetworkAttaching, nil, true},
		{client.PrivateNetworkAttached, ptr("10.250.0.7"), true},
		{client.PrivateNetworkDetaching, ptr("10.250.0.7"), false},
	} {
		inst := running("vm_1", "web")
		inst.PrivateNetworkState, inst.PrivateNetworkIP = tt.state, tt.ip
		m, _ := fromAPIResponse(ctx, model{}, &inst)
		if m.PrivateNetwork.ValueBool() != tt.wantOn || (tt.ip != nil) == m.PrivateNetIP.IsNull() {
			t.Errorf("%q: private_network = %v, ip = %v", tt.state, m.PrivateNetwork, m.PrivateNetIP)
		}
	}
}

func TestValidateConfigRejectsThePrivateNetworkInAVPC(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
	}{
		{"standard", map[string]tftypes.Value{"private_network": boolean(true)}, false},
		{"vpc without it", map[string]tftypes.Value{"subnet_id": str("snet_1"), "private_network": boolean(false)}, false},
		{"vpc with it", map[string]tftypes.Value{"subnet_id": str("snet_1"), "private_network": boolean(true)}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: values(s, tt.set)}}, resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %s", errorText(resp.Diagnostics))
			}
		})
	}
}
