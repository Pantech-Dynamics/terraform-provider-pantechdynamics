package cloudconnectionfederation

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

type fakeAPI struct {
	final   string
	sent    []client.UpdateCloudConnectionRequest
	waits   int
	missing bool
	conn    client.CloudConnection
}

func (f *fakeAPI) GetCloudConnection(context.Context, string) (*client.CloudConnection, error) {
	if f.missing {
		return nil, &client.APIError{Status: 404, Code: client.CodeCloudConnectionNotFound}
	}
	return &f.conn, nil
}

func (f *fakeAPI) UpdateCloudConnection(_ context.Context, id string, req client.UpdateCloudConnectionRequest) (*client.CloudConnection, error) {
	f.sent = append(f.sent, req)
	f.conn = client.CloudConnection{ID: id, Status: client.CloudConnectionVerifying, RoleARN: req.RoleARN, GCP: req.GCP}
	return &f.conn, nil
}

func (f *fakeAPI) WaitForCloudConnectionCheck(context.Context, string) (*client.CloudConnection, error) {
	f.waits++
	acct := "123456789012"
	f.conn.Status, f.conn.ExternalAccountID = f.final, &acct
	if f.final == client.CloudConnectionFailed {
		f.conn.Failure = &client.CloudFailure{Code: "assume_role_denied", Message: "Check the role's trust policy."}
	}
	return &f.conn, nil
}

func timeoutsNull(t *testing.T) tftypes.Value {
	s := SchemaOf(t, New())
	return tftypes.NewValue(attrType(t, s, "timeouts"), nil)
}

func create(t *testing.T, api *fakeAPI, set map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := SchemaOf(t, New())
	set["timeouts"] = timeoutsNull(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: Plan(s, set)}, resp)
	return resp
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func TestCreateSendsTheRoleAndWaits(t *testing.T) {
	api := &fakeAPI{final: client.CloudConnectionConnected}
	resp := create(t, api, map[string]tftypes.Value{"connection_id": Str("ccn_1"), "role_arn": Str("arn:aws:iam::123456789012:role/PantechRead")})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if len(api.sent) != 1 || *api.sent[0].RoleARN != "arn:aws:iam::123456789012:role/PantechRead" || api.sent[0].GCP != nil || api.waits != 1 ||
		m.Status.ValueString() != "connected" || m.ExternalAccountID.ValueString() != "123456789012" || m.ID.ValueString() != "ccn_1" {
		t.Fatalf("sent %+v, model %+v", api.sent, m)
	}
}

func TestCreateSendsAGCPFederation(t *testing.T) {
	api := &fakeAPI{final: client.CloudConnectionConnected}
	s := SchemaOf(t, New())
	gcpType := attrType(t, s, "gcp")
	gcp := tftypes.NewValue(gcpType, map[string]tftypes.Value{
		"project_number": Str("123"), "pool_id": Str("pantech-migrate"), "provider_id": Str("ccn-1"), "service_account_email": Str("sa@example.test"),
		"project_ids": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{Str("web"), Str("api")}),
	})
	resp := create(t, api, map[string]tftypes.Value{"connection_id": Str("ccn_1"), "gcp": gcp})
	if resp.Diagnostics.HasError() || api.sent[0].GCP == nil || strings.Join(api.sent[0].GCP.ProjectIDs, ",") != "web,api" || api.sent[0].RoleARN != nil {
		t.Fatalf("diagnostics %v, sent %+v", resp.Diagnostics, api.sent)
	}
}

func TestAFailedCheckStopsTheApplyWithStateSaved(t *testing.T) {
	api := &fakeAPI{final: client.CloudConnectionFailed}
	resp := create(t, api, map[string]tftypes.Value{"connection_id": Str("ccn_1"), "role_arn": Str("arn:aws:iam::123456789012:role/x")})
	if !strings.Contains(ErrorText(resp.Diagnostics), "trust policy") || IsRemoved(resp.State) || getModel(t, resp.State).Status.ValueString() != "failed" {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestReadFollowsTheConnection(t *testing.T) {
	s := SchemaOf(t, New())
	st := State(s, map[string]tftypes.Value{"id": Str("ccn_1"), "connection_id": Str("ccn_1"), "role_arn": Str("arn:aws:iam::123456789012:role/old"), "timeouts": timeoutsNull(t)})
	other := "arn:aws:iam::123456789012:role/changed"
	api := &fakeAPI{conn: client.CloudConnection{ID: "ccn_1", Status: client.CloudConnectionConnected, RoleARN: &other}}
	resp := &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || getModel(t, resp.State).RoleARN.ValueString() != other {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	api.missing = true
	resp = &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if !IsRemoved(resp.State) {
		t.Fatal("a deleted connection stayed in state")
	}
}

func TestValidateConfigWantsExactlyOne(t *testing.T) {
	s := SchemaOf(t, New())
	for name, set := range map[string]map[string]tftypes.Value{
		"none": {"connection_id": Str("ccn_1")},
		"two":  {"connection_id": Str("ccn_1"), "role_arn": Str("arn"), "azure": azure(t, s)},
	} {
		set["timeouts"] = timeoutsNull(t)
		resp := &resource.ValidateConfigResponse{}
		(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: Object(s, set)}}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("%s: accepted", name)
		}
	}
	resp := &resource.ValidateConfigResponse{}
	cfg := Object(s, map[string]tftypes.Value{"connection_id": Str("ccn_1"), "azure": azure(t, s), "timeouts": timeoutsNull(t)})
	(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: cfg}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("azure alone: %v", resp.Diagnostics)
	}
}

func azure(t *testing.T, s schema.Schema) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(attrType(t, s, "azure"), map[string]tftypes.Value{
		"tenant_id": Str("t"), "client_id": Str("c"),
		"subscription_ids": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{Str("s")}),
	})
}

// attrType is the Terraform type of one of the schema's attributes.
func attrType(t *testing.T, s schema.Schema, name string) tftypes.Type {
	t.Helper()
	obj, ok := s.Type().TerraformType(Ctx).(tftypes.Object)
	if !ok {
		t.Fatal("the schema type is not an object")
	}
	return obj.AttributeTypes[name]
}
