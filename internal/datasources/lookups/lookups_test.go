package lookups

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

// fakeAPI serves every list the lookups need.
type fakeAPI struct {
	groups    []client.SecurityGroup
	keys      []client.SSHKey
	networks  []client.Network
	instances []client.Instance
	lbs       []client.LoadBalancer
	clusters  []client.KubernetesCluster
	err       error
}

func (f *fakeAPI) ListKubernetesClusters(context.Context) ([]client.KubernetesCluster, error) {
	return f.clusters, f.err
}

func (f *fakeAPI) ListLoadBalancers(context.Context) ([]client.LoadBalancer, error) {
	return f.lbs, f.err
}

func (f *fakeAPI) ListSecurityGroups(context.Context) ([]client.SecurityGroup, error) {
	return f.groups, f.err
}
func (f *fakeAPI) ListSSHKeys(context.Context) ([]client.SSHKey, error)   { return f.keys, f.err }
func (f *fakeAPI) ListNetworks(context.Context) ([]client.Network, error) { return f.networks, f.err }
func (f *fakeAPI) ListInstances(context.Context) ([]client.Instance, error) {
	return f.instances, f.err
}

func ptr(s string) *string { return &s }

func fixture() *fakeAPI {
	return &fakeAPI{
		groups: []client.SecurityGroup{
			{ID: "sg_1", Name: "default", Rules: []client.SecurityGroupRule{{Direction: "ingress", Protocol: "icmp", CIDR: "0.0.0.0/0"}}, ObservedState: "active"},
			{ID: "sg_2", Name: "web", ObservedState: "active"},
		},
		keys:     []client.SSHKey{{ID: "sshk_1", Name: "laptop", Fingerprint: "SHA256:x", PublicKey: "ssh-ed25519 AAAA"}},
		networks: []client.Network{{ID: "net_1", Name: "main", CIDR: "10.0.0.0/16"}, {ID: "net_2", Name: "dup"}, {ID: "net_3", Name: "dup"}},
		instances: []client.Instance{
			{ID: "vm_1", Name: "web", ObservedState: "running", SubnetID: ptr("snet_1"), Tags: map[string]string{"env": "prod"}},
			{ID: "vm_2", Name: "old", ObservedState: client.InstanceDeleted},
		},
		lbs: []client.LoadBalancer{
			{ID: "lb_1", Name: "web", PublicIPID: "pip_1", PublicIPAddress: ptr("203.0.113.9"), SubnetID: "snet_1", Protocol: "tcp", Algorithm: "roundrobin",
				PublicPort: 80, PrivatePort: 8080, ObservedState: "active", Members: []client.LoadBalancerMember{
					{InstanceID: "vm_2", DesiredState: "present"}, {InstanceID: "vm_1", DesiredState: "present"}, {InstanceID: "vm_3", DesiredState: "deleted"},
				}},
		},
		clusters: []client.KubernetesCluster{
			{ID: "k8s_1", Name: "prod", ZoneID: "af-abj-2", KubernetesVersionID: "k8sv_1", KubernetesVersion: "1.31.2", NetworkID: ptr("net_1"), SubnetID: ptr("snet_1"),
				NodePlanID: "plan_1", Node: client.KubernetesNode{VCPU: 2, MemoryMB: 4096, DiskGB: 40}, ControlNodes: 1, Workers: 2, Nodes: 3,
				ObservedState: "running", InSync: true, AvailableUpgrades: []client.KubernetesVersion{{ID: "k8sv_2"}},
				Autoscaling: client.KubernetesAutoscaling{Enabled: true, MinWorkers: 2, MaxWorkers: 5}, APIAllowedCIDRs: []string{"203.0.113.0/24"},
				Endpoint: ptr("https://k8s-1.example.test:6443"), VolumeStorageGB: 30},
		},
	}
}

// read drives a data source's ValidateConfig and Read with the given selector.
func read(t *testing.T, ds datasource.DataSourceWithValidateConfig, sel map[string]string) datasource.ReadResponse {
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
	for k, v := range sel {
		vals[k] = tftypes.NewValue(tftypes.String, v)
	}
	cfg := tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}

	var vresp datasource.ValidateConfigResponse
	ds.ValidateConfig(ctx, datasource.ValidateConfigRequest{Config: cfg}, &vresp)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	resp.Diagnostics.Append(vresp.Diagnostics...)
	if !resp.Diagnostics.HasError() {
		ds.Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	}
	return resp
}

func errText(resp datasource.ReadResponse) string {
	var b strings.Builder
	for _, e := range resp.Diagnostics.Errors() {
		b.WriteString(e.Summary() + " " + e.Detail() + "\n")
	}
	return b.String()
}

func TestLookups(t *testing.T) {
	api := fixture()
	tests := []struct {
		name    string
		ds      datasource.DataSourceWithValidateConfig
		sel     map[string]string
		wantID  string
		wantErr string
	}{
		{"security group by name", &SecurityGroupDataSource{api: api}, map[string]string{"name": "default"}, "sg_1", ""},
		{"security group by id", &SecurityGroupDataSource{api: api}, map[string]string{"id": "sg_2"}, "sg_2", ""},
		{"security group unknown", &SecurityGroupDataSource{api: api}, map[string]string{"name": "nope"}, "", "Available names: default, web."},
		{"ssh key by name", &SSHKeyDataSource{api: api}, map[string]string{"name": "laptop"}, "sshk_1", ""},
		{"network by name", &NetworkDataSource{api: api}, map[string]string{"name": "main"}, "net_1", ""},
		{"network name ambiguous", &NetworkDataSource{api: api}, map[string]string{"name": "dup"}, "", "net_2, net_3"},
		{"instance by name", &InstanceDataSource{api: api}, map[string]string{"name": "web"}, "vm_1", ""},
		{"deleted instance is not found", &InstanceDataSource{api: api}, map[string]string{"id": "vm_2"}, "", "Instance not found"},
		{"load balancer by name", &LoadBalancerDataSource{api: api}, map[string]string{"name": "web"}, "lb_1", ""},
		{"load balancer unknown id", &LoadBalancerDataSource{api: api}, map[string]string{"id": "lb_9"}, "", "Available ids: lb_1."},
		{"kubernetes cluster by name", &KubernetesClusterDataSource{api: api}, map[string]string{"name": "prod"}, "k8s_1", ""},
		{"kubernetes cluster unknown id", &KubernetesClusterDataSource{api: api}, map[string]string{"id": "k8s_9"}, "", "Available ids: k8s_1."},
		{"both selectors", &NetworkDataSource{api: api}, map[string]string{"id": "net_1", "name": "main"}, "", "exactly one"},
		{"no selector", &SSHKeyDataSource{api: api}, map[string]string{}, "", "exactly one"},
		{"api error", &SSHKeyDataSource{api: &fakeAPI{err: errors.New("boom")}}, map[string]string{"name": "x"}, "", "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := read(t, tt.ds, tt.sel)
			if tt.wantErr != "" {
				if !strings.Contains(errText(resp), tt.wantErr) {
					t.Fatalf("errors = %s, want %q", errText(resp), tt.wantErr)
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var id string
			resp.Diagnostics.Append(resp.State.GetAttribute(ctx, pathID, &id)...)
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestInstanceLookupMapsTheDetails(t *testing.T) {
	resp := read(t, &InstanceDataSource{api: fixture()}, map[string]string{"id": "vm_1"})
	var m instanceModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if m.SubnetID.ValueString() != "snet_1" || !m.NetworkID.IsNull() || len(m.Tags.Elements()) != 1 {
		t.Errorf("model = %+v", m)
	}
}

func TestSecurityGroupLookupMapsRules(t *testing.T) {
	resp := read(t, &SecurityGroupDataSource{api: fixture()}, map[string]string{"name": "default"})
	var m securityGroupModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if len(m.Rules) != 1 || m.Rules[0].Protocol.ValueString() != "icmp" || m.Rules[0].PortRange.ValueString() != "" {
		t.Errorf("rules = %+v", m.Rules)
	}
}

func TestLoadBalancerLookupMapsTheDetails(t *testing.T) {
	resp := read(t, &LoadBalancerDataSource{api: fixture()}, map[string]string{"id": "lb_1"})
	var m loadBalancerModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var targets []string
	resp.Diagnostics.Append(m.InstanceIDs.ElementsAs(ctx, &targets, false)...)
	if m.PrivatePort.ValueInt64() != 8080 || m.PublicIPAddress.ValueString() != "203.0.113.9" || !m.NetworkID.IsNull() ||
		len(m.CIDRList.Elements()) != 0 || len(targets) != 2 {
		t.Errorf("model = %+v, targets %v", m, targets)
	}
}

func TestKubernetesClusterLookupMapsTheDetails(t *testing.T) {
	resp := read(t, &KubernetesClusterDataSource{api: fixture()}, map[string]string{"id": "k8s_1"})
	var m kubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var upgrades []string
	resp.Diagnostics.Append(m.AvailableUpgradeIDs.ElementsAs(ctx, &upgrades, false)...)
	if m.Nodes.ValueInt64() != 3 || m.NetworkID.ValueString() != "net_1" || m.KubernetesVersion.ValueString() != "1.31.2" ||
		len(upgrades) != 1 || upgrades[0] != "k8sv_2" || len(m.Node.Attributes()) != 3 {
		t.Errorf("model = %+v", m)
	}
	auto := m.Autoscaling.Attributes()
	if !auto["enabled"].Equal(types.BoolValue(true)) || !auto["min_workers"].Equal(types.Int64Value(2)) || !auto["max_workers"].Equal(types.Int64Value(5)) ||
		len(m.APIAllowedCIDRs.Elements()) != 1 || m.Endpoint.ValueString() != "https://k8s-1.example.test:6443" || m.VolumeStorageGB.ValueInt64() != 30 {
		t.Errorf("new fields: autoscaling %v, cidrs %v, endpoint %v, volume %v", auto, m.APIAllowedCIDRs, m.Endpoint, m.VolumeStorageGB)
	}
}
