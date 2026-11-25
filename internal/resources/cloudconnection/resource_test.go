package cloudconnection

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

type fakeAPI struct {
	conn       *client.CloudConnection
	createErr  error
	lastCreate client.CreateCloudConnectionRequest
	lastUpdate client.UpdateCloudConnectionRequest
	deletes    int
}

func (f *fakeAPI) CreateCloudConnection(_ context.Context, req client.CreateCloudConnectionRequest) (*client.CloudConnection, error) {
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.conn = agentConnection(req.Provider, req.Name)
	return f.conn, nil
}

func (f *fakeAPI) GetCloudConnection(context.Context, string) (*client.CloudConnection, error) {
	if f.conn == nil {
		return nil, &client.APIError{Status: 404, Code: client.CodeCloudConnectionNotFound}
	}
	return f.conn, nil
}

func (f *fakeAPI) UpdateCloudConnection(_ context.Context, _ string, req client.UpdateCloudConnectionRequest) (*client.CloudConnection, error) {
	f.lastUpdate = req
	f.conn.Name = *req.Name
	return f.conn, nil
}

func (f *fakeAPI) DeleteCloudConnection(context.Context, string) error {
	f.deletes++
	if f.conn == nil {
		return &client.APIError{Status: 404, Code: client.CodeCloudConnectionNotFound}
	}
	f.conn = nil
	return nil
}

func agentConnection(provider, name string) *client.CloudConnection {
	created := time.Date(2026, 11, 25, 10, 0, 0, 500000000, time.UTC)
	return &client.CloudConnection{ID: "ccn_1", Provider: provider, Name: name, Status: client.CloudConnectionAwaitingAgent, CreatedAt: &created,
		Setup: client.CloudConnectionSetup{Access: client.CloudAccessAgent, Subject: "ccn_1", Template: "Make a read-only token.", TemplateFormat: "agent",
			Files: []client.CloudSetupFile{{Name: "agent.env", Content: "HCLOUD_TOKEN="}}, Permissions: []string{"read"},
			Agent: &client.CloudAgentSetup{Image: "agent:1", TokenVariables: []string{"HCLOUD_TOKEN"}, EnrollCommand: "enroll <ENROLLMENT_CODE>", RunCommand: "run"}}}
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI, cloud string) *resource.CreateResponse {
	t.Helper()
	s := SchemaOf(t, New())
	set := map[string]tftypes.Value{"name": Str("Hetzner"), "cloud": Str(cloud)}
	plan := Plan(s, set)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: plan}, resp)
	return resp
}

func TestCreateKeepsTheSetup(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, "hetzner")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if api.lastCreate.Provider != "hetzner" || m.Status.ValueString() != "awaiting_agent" || m.Access.ValueString() != "agent" ||
		m.SetupFiles.Elements()["agent.env"] == nil || m.AgentEnrollCommand.ValueString() != "enroll <ENROLLMENT_CODE>" ||
		len(m.AgentTokenVariables.Elements()) != 1 || !m.IssuerURL.IsNull() || m.CreatedAt.ValueString() != "2026-11-25T10:00:00Z" {
		t.Fatalf("model = %+v", m)
	}
}

func TestCreateRefusalIsExplained(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 503, Code: client.CodeMigrateUnavailable}}
	resp := create(t, api, "aws")
	if !strings.Contains(ErrorText(resp.Diagnostics), "not available on this platform") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestReadRemovesAConnectionGoneOutsideTerraform(t *testing.T) {
	s := SchemaOf(t, New())
	resp := &resource.ReadResponse{State: State(s, map[string]tftypes.Value{"id": Str("ccn_1"), "name": Str("x"), "cloud": Str("aws")})}
	(&Resource{api: &fakeAPI{}}).Read(Ctx, resource.ReadRequest{State: resp.State}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestUpdateRenamesAndDeleteDisconnects(t *testing.T) {
	api := &fakeAPI{conn: agentConnection("hetzner", "old")}
	s := SchemaOf(t, New())
	st := State(s, map[string]tftypes.Value{"id": Str("ccn_1"), "name": Str("old"), "cloud": Str("hetzner")})
	uresp := &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: Plan(s, map[string]tftypes.Value{"id": Str("ccn_1"), "name": Str("new"), "cloud": Str("hetzner")}), State: st}, uresp)
	if uresp.Diagnostics.HasError() || *api.lastUpdate.Name != "new" || getModel(t, uresp.State).Name.ValueString() != "new" {
		t.Fatalf("update: %v %+v", uresp.Diagnostics, api.lastUpdate)
	}
	for range 2 { // a second delete finds it gone, and that is success
		dresp := &resource.DeleteResponse{State: st}
		(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, dresp)
		if dresp.Diagnostics.HasError() || len(dresp.Diagnostics.Warnings()) != 1 {
			t.Fatalf("delete: %v", dresp.Diagnostics)
		}
	}
	if api.deletes != 2 {
		t.Fatalf("deletes = %d", api.deletes)
	}
}

func TestImportChecksTheID(t *testing.T) {
	s := SchemaOf(t, New())
	resp := &resource.ImportStateResponse{State: EmptyState(s)}
	(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: "vm_1"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a wrong id was imported")
	}
}
