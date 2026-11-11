package accesspolicyattachment

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

type fakeAPI struct {
	attachments map[string][]client.IAMAttachment
	attachErr   error
	attaches    int
}

func (f *fakeAPI) ListIAMPolicyAttachments(_ context.Context, policyID string) ([]client.IAMAttachment, error) {
	list, ok := f.attachments[policyID]
	if !ok {
		return nil, &client.APIError{Status: 404, Code: client.CodeIAMPolicyNotFound}
	}
	return list, nil
}

func (f *fakeAPI) AttachIAMPolicy(_ context.Context, policyID, typ, principal string) (*client.IAMAttachment, error) {
	f.attaches++
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	a := client.IAMAttachment{PolicyID: policyID, PrincipalType: typ, PrincipalID: principal}
	f.attachments[policyID] = append(f.attachments[policyID], a)
	return &a, nil
}

func (f *fakeAPI) DetachIAMPolicy(_ context.Context, policyID, typ, principal string) error {
	for i, a := range f.attachments[policyID] {
		if a.PrincipalType == typ && a.PrincipalID == principal {
			f.attachments[policyID] = append(f.attachments[policyID][:i], f.attachments[policyID][i+1:]...)
			return nil
		}
	}
	return &client.APIError{Status: 404, Code: client.CodeIAMAttachmentNotFound}
}

func plan(t *testing.T, policyID, typ, principal string) tfsdk.Plan {
	s := SchemaOf(t, New())
	return Plan(s, map[string]tftypes.Value{"id": UnknownStr(), "policy_id": Str(policyID), "principal_type": Str(typ), "principal_id": Str(principal), "attached_at": UnknownStr()})
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}

// A managed policy attaches like any other.
func TestAttachAManagedPolicyToATeam(t *testing.T) {
	api := &fakeAPI{attachments: map[string][]client.IAMAttachment{"pol_pantech_developer": nil}}
	s := SchemaOf(t, New())
	r := &Resource{api: api}
	cresp := &resource.CreateResponse{State: EmptyState(s)}
	r.Create(Ctx, resource.CreateRequest{Plan: plan(t, "pol_pantech_developer", "team", "team_1")}, cresp)
	if cresp.Diagnostics.HasError() || getModel(t, cresp.State).ID.ValueString() != "pol_pantech_developer/team/team_1" {
		t.Fatalf("diagnostics = %v", cresp.Diagnostics)
	}
	// Detached outside Terraform: Read drops it; the policy gone: also dropped.
	api.attachments["pol_pantech_developer"] = nil
	rresp := &resource.ReadResponse{State: cresp.State}
	r.Read(Ctx, resource.ReadRequest{State: cresp.State}, rresp)
	if !IsRemoved(rresp.State) {
		t.Fatal("a detached policy must leave state")
	}
	dresp := &resource.DeleteResponse{State: cresp.State}
	r.Delete(Ctx, resource.DeleteRequest{State: cresp.State}, dresp)
	if dresp.Diagnostics.HasError() {
		t.Fatal(dresp.Diagnostics)
	}
}

// A refused attach of a policy that is in fact already attached is adopted.
func TestAttachAdoptsAnExistingAttachment(t *testing.T) {
	api := &fakeAPI{attachments: map[string][]client.IAMAttachment{"pol_1": {{PolicyID: "pol_1", PrincipalType: "member", PrincipalID: "usr_1"}}},
		attachErr: &client.APIError{Status: 409, Code: "CONFLICT"}}
	resp := &resource.CreateResponse{State: EmptyState(SchemaOf(t, New()))}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: plan(t, "pol_1", "member", "usr_1")}, resp)
	if resp.Diagnostics.HasError() || getModel(t, resp.State).PrincipalID.ValueString() != "usr_1" {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	api.attachErr = &client.APIError{Status: 403, Code: "IAM_ATTACHMENT_LIMIT_REACHED"}
	resp = &resource.CreateResponse{State: EmptyState(SchemaOf(t, New()))}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: plan(t, "pol_1", "member", "usr_2")}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "(10)") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestValidateConfigChecksThePrincipalPrefix(t *testing.T) {
	s := SchemaOf(t, New())
	for _, tt := range []struct {
		typ, id string
		wantErr bool
	}{{"member", "usr_1", false}, {"team", "team_1", false}, {"member", "team_1", true}, {"team", "usr_1", true}} {
		cfg := tfsdk.Config{Schema: s, Raw: Object(s, map[string]tftypes.Value{"policy_id": Str("pol_1"), "principal_type": Str(tt.typ), "principal_id": Str(tt.id)})}
		resp := &resource.ValidateConfigResponse{}
		(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
		if resp.Diagnostics.HasError() != tt.wantErr {
			t.Errorf("%s %s: diagnostics = %v", tt.typ, tt.id, resp.Diagnostics)
		}
	}
}

func TestImport(t *testing.T) {
	s := SchemaOf(t, New())
	for id, ok := range map[string]bool{"pol_pantech_developer/team/team_1": true, "pol_1/member/usr_1": true, "pol_1/member/team_1": false, "pol_1/user/usr_1": false, "pol_1": false} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("%s: diagnostics = %v", id, resp.Diagnostics)
		}
	}
}
