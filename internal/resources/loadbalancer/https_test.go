package loadbalancer

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// httpsArgs is an appliance https listener on 443 with cert_1, as planned.
func httpsArgs(cert, policy string, redirect bool) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"name": Str("web"), "kind": Str(client.LoadBalancerAppliance), "zone_id": Str("af-abj-1"), "algorithm": Str("roundrobin"),
		"public_port": Num(443), "private_port": Num(80), "cidr_list": strSet(), "instance_ids": strSet(),
		"protocol": Str(client.LoadBalancerHTTPS), "certificate_id": Str(cert), "tls_policy": Str(policy), "redirect_http": tftypes.NewValue(tftypes.Bool, redirect),
	}
}

func httpsPlan(s schema.Schema, a map[string]tftypes.Value) tfsdk.Plan {
	for _, k := range []string{"id", "public_ip_id", "subnet_id", "public_ip_address", "network_id", "observed_state", "created_at", "updated_at", "certificate_fingerprint_sha256"} {
		a[k] = UnknownStr()
	}
	return Plan(s, a)
}

func TestCreateHTTPSListener(t *testing.T) {
	api := &fakeAPI{}
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: httpsPlan(s, httpsArgs("cert_1", "tls13", true))}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	c := api.lastCreate
	if c.Protocol != "https" || c.CertificateID != "cert_1" || c.TLSPolicy != "tls13" || c.RedirectHTTP == nil || !*c.RedirectHTTP {
		t.Fatalf("sent %+v", c)
	}
	m := getModel(t, resp.State)
	if m.ServedCertFP.ValueString() != "fp_cert_1" || m.TLSPolicy.ValueString() != "tls13" || !m.RedirectHTTP.ValueBool() || m.PrivatePort.ValueInt64() != 80 {
		t.Fatalf("model = %+v", m)
	}
}

// A tcp listener sends no TLS settings, which the backend would refuse.
func TestTCPListenerSendsNoTLSSettings(t *testing.T) {
	api := &fakeAPI{}
	s := testSchema(t)
	a := args("web", "roundrobin", strSet())
	a["protocol"], a["redirect_http"] = Str("tcp"), tftypes.NewValue(tftypes.Bool, false)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, a)}, resp)
	if c := api.lastCreate; c.Protocol != "" || c.CertificateID != "" || c.TLSPolicy != "" || c.RedirectHTTP != nil {
		t.Fatalf("sent %+v", c)
	}
}

// The certificate, the policy and the redirect change in place.
func TestUpdateHTTPSSettingsInPlace(t *testing.T) {
	api := &fakeAPI{}
	s := testSchema(t)
	cresp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: httpsPlan(s, httpsArgs("cert_1", "modern", false))}, cresp)
	plan := httpsPlan(s, httpsArgs("cert_2", "tls13", true))
	uresp := &resource.UpdateResponse{State: cresp.State}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: cresp.State}, uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	u := api.lastUpdate
	if u.CertificateID == nil || *u.CertificateID != "cert_2" || u.TLSPolicy == nil || *u.TLSPolicy != "tls13" || u.RedirectHTTP == nil || !*u.RedirectHTTP {
		t.Fatalf("sent %+v", u)
	}
	if m := getModel(t, uresp.State); m.ServedCertFP.ValueString() != "fp_cert_2" {
		t.Fatalf("model = %+v", m)
	}
}

func TestValidateConfigListener(t *testing.T) {
	s := testSchema(t)
	null := tftypes.NewValue(tftypes.String, nil)
	tests := []struct {
		name string
		set  map[string]tftypes.Value
		want string
	}{
		{"https appliance", map[string]tftypes.Value{"zone_id": Str("af-abj-1"), "protocol": Str("https"), "certificate_id": Str("cert_1"), "public_port": Num(443)}, ""},
		{"https vpc_native", map[string]tftypes.Value{"public_ip_id": Str("pip_1"), "subnet_id": Str("snet_1"), "protocol": Str("https"), "certificate_id": Str("cert_1"), "public_port": Num(443)}, "HTTPS not supported here"},
		{"https without certificate", map[string]tftypes.Value{"zone_id": Str("af-abj-1"), "protocol": Str("https"), "certificate_id": null, "public_port": Num(443)}, "Missing certificate_id"},
		{"certificate on tcp", map[string]tftypes.Value{"zone_id": Str("af-abj-1"), "certificate_id": Str("cert_1"), "public_port": Num(443)}, "HTTPS setting on a tcp listener"},
		{"redirect on 80", map[string]tftypes.Value{"zone_id": Str("af-abj-1"), "protocol": Str("https"), "certificate_id": Str("cert_1"), "public_port": Num(80), "redirect_http": tftypes.NewValue(tftypes.Bool, true)}, "Port 80 is the redirect"},
		{"reserved appliance port", map[string]tftypes.Value{"zone_id": Str("af-abj-1"), "public_port": Num(8404)}, "Reserved port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.set["name"] = Str("web")
			cfg := tfsdk.Config{Schema: s, Raw: Object(s, tt.set)}
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
			got := ErrorText(resp.Diagnostics)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("errors = %q, want %q", got, tt.want)
			}
		})
	}
}
