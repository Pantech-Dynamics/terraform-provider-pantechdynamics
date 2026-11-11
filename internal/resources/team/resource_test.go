package team

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory team and membership API.
type fakeAPI struct {
	teams      map[string]client.IAMTeam
	members    map[string][]client.IAMTeamMember
	createErr  error
	addErr     error
	lastUpdate client.UpdateIAMTeamRequest
}

func newFake() *fakeAPI {
	return &fakeAPI{teams: map[string]client.IAMTeam{}, members: map[string][]client.IAMTeamMember{}}
}

func teamGone() error {
	return fmt.Errorf("team: %w", &client.APIError{Status: 404, Code: client.CodeIAMTeamNotFound})
}

func (f *fakeAPI) CreateIAMTeam(_ context.Context, req client.CreateIAMTeamRequest) (*client.IAMTeam, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	t := client.IAMTeam{ID: "team_1", Name: req.Name, Description: req.Description}
	f.teams[t.ID] = t
	return &t, nil
}

func (f *fakeAPI) GetIAMTeam(_ context.Context, id string) (*client.IAMTeam, error) {
	t, ok := f.teams[id]
	if !ok {
		return nil, teamGone()
	}
	t.MemberCount = int64(len(f.members[id]))
	return &t, nil
}

func (f *fakeAPI) UpdateIAMTeam(_ context.Context, id string, req client.UpdateIAMTeamRequest) (*client.IAMTeam, error) {
	f.lastUpdate = req
	t := f.teams[id]
	if req.Name != nil {
		t.Name = *req.Name
	}
	if req.Description != nil {
		t.Description = *req.Description
	}
	f.teams[id] = t
	return &t, nil
}

func (f *fakeAPI) DeleteIAMTeam(_ context.Context, id string) error {
	if _, ok := f.teams[id]; !ok {
		return teamGone()
	}
	delete(f.teams, id)
	delete(f.members, id)
	return nil
}

func (f *fakeAPI) ListIAMTeamMembers(_ context.Context, teamID string) ([]client.IAMTeamMember, error) {
	if _, ok := f.teams[teamID]; !ok {
		return nil, teamGone()
	}
	return f.members[teamID], nil
}

func (f *fakeAPI) AddIAMTeamMember(_ context.Context, teamID, userID string) (*client.IAMTeamMember, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	for _, m := range f.members[teamID] {
		if m.UserID == userID {
			return &m, nil // already in: the PUT is idempotent
		}
	}
	m := client.IAMTeamMember{UserID: userID, Email: "ada@example.com", Role: "member"}
	f.members[teamID] = append(f.members[teamID], m)
	return &m, nil
}

func (f *fakeAPI) RemoveIAMTeamMember(_ context.Context, teamID, userID string) error {
	for i, m := range f.members[teamID] {
		if m.UserID == userID {
			f.members[teamID] = append(f.members[teamID][:i], f.members[teamID][i+1:]...)
			return nil
		}
	}
	return &client.APIError{Status: 404, Code: "RESOURCE_NOT_FOUND"}
}

func teamPlan(t *testing.T, id tftypes.Value, name, description string) tfsdk.Plan {
	s := SchemaOf(t, New())
	return Plan(s, map[string]tftypes.Value{"id": id, "name": Str(name), "description": Str(description), "member_count": Unknown(tftypes.Number), "created_at": UnknownStr()})
}

func TestTeamLifecycle(t *testing.T) {
	api := newFake()
	s := SchemaOf(t, New())
	r := &Resource{api: api}

	cresp := &resource.CreateResponse{State: EmptyState(s)}
	r.Create(Ctx, resource.CreateRequest{Plan: teamPlan(t, UnknownStr(), "Platform", "")}, cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}

	uresp := &resource.UpdateResponse{State: cresp.State}
	r.Update(Ctx, resource.UpdateRequest{Plan: teamPlan(t, Str("team_1"), "Platform", "Infra people"), State: cresp.State}, uresp)
	if uresp.Diagnostics.HasError() || api.lastUpdate.Name != nil || *api.lastUpdate.Description != "Infra people" {
		t.Fatalf("diagnostics = %v, sent %+v", uresp.Diagnostics, api.lastUpdate)
	}

	api.members["team_1"] = []client.IAMTeamMember{{UserID: "usr_1"}, {UserID: "usr_2"}}
	rresp := &resource.ReadResponse{State: uresp.State}
	r.Read(Ctx, resource.ReadRequest{State: uresp.State}, rresp)
	var m model
	rresp.State.Get(Ctx, &m)
	if m.MemberCount.ValueInt64() != 2 || m.Description.ValueString() != "Infra people" {
		t.Fatalf("model = %+v", m)
	}

	for range 2 { // gone the second time: still success
		dresp := &resource.DeleteResponse{State: rresp.State}
		r.Delete(Ctx, resource.DeleteRequest{State: rresp.State}, dresp)
		if dresp.Diagnostics.HasError() {
			t.Fatal(dresp.Diagnostics)
		}
	}
	rresp = &resource.ReadResponse{State: uresp.State}
	r.Read(Ctx, resource.ReadRequest{State: uresp.State}, rresp)
	if !IsRemoved(rresp.State) {
		t.Fatal("a deleted team must leave state")
	}
}

func TestTeamCreateRefusalsAreExplained(t *testing.T) {
	api := newFake()
	api.createErr = &client.APIError{Status: 409, Code: "IAM_TEAM_NAME_TAKEN"}
	s := SchemaOf(t, New())
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: teamPlan(t, UnknownStr(), "Platform", "")}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "Choose another") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestTeamNameValidator(t *testing.T) {
	for name, ok := range map[string]bool{"Platform": true, "Équipe 1": true, "": false, " lead": false, "trail ": false, "tab\there": false, strings.Repeat("a", 65): false} {
		var r validator.StringResponse
		teamName{}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("name"), ConfigValue: types.StringValue(name)}, &r)
		if r.Diagnostics.HasError() == ok {
			t.Errorf("%q: error = %v", name, r.Diagnostics.HasError())
		}
	}
}

func membershipPlan(t *testing.T, teamID, userID string) tfsdk.Plan {
	s := SchemaOf(t, NewMembership())
	return Plan(s, map[string]tftypes.Value{"id": UnknownStr(), "team_id": Str(teamID), "user_id": Str(userID), "email": UnknownStr(), "role": UnknownStr(), "added_at": UnknownStr()})
}

func TestMembershipLifecycle(t *testing.T) {
	api := newFake()
	api.teams["team_1"] = client.IAMTeam{ID: "team_1"}
	s := SchemaOf(t, NewMembership())
	r := &MembershipResource{api: api}

	cresp := &resource.CreateResponse{State: EmptyState(s)}
	r.Create(Ctx, resource.CreateRequest{Plan: membershipPlan(t, "team_1", "usr_1")}, cresp)
	var m membershipModel
	cresp.State.Get(Ctx, &m)
	if cresp.Diagnostics.HasError() || m.ID.ValueString() != "team_1/usr_1" || m.Role.ValueString() != "member" {
		t.Fatalf("diagnostics = %v, model = %+v", cresp.Diagnostics, m)
	}
	// Removed outside Terraform: Read drops it.
	api.members["team_1"] = nil
	rresp := &resource.ReadResponse{State: cresp.State}
	r.Read(Ctx, resource.ReadRequest{State: cresp.State}, rresp)
	if !IsRemoved(rresp.State) {
		t.Fatal("a removed membership must leave state")
	}
	// The team itself gone: also dropped, not an error.
	delete(api.teams, "team_1")
	rresp = &resource.ReadResponse{State: cresp.State}
	r.Read(Ctx, resource.ReadRequest{State: cresp.State}, rresp)
	if rresp.Diagnostics.HasError() || !IsRemoved(rresp.State) {
		t.Fatalf("diagnostics = %v", rresp.Diagnostics)
	}
	dresp := &resource.DeleteResponse{State: cresp.State}
	r.Delete(Ctx, resource.DeleteRequest{State: cresp.State}, dresp)
	if dresp.Diagnostics.HasError() {
		t.Fatal(dresp.Diagnostics)
	}
}

func TestMembershipAdminTargetIsExplained(t *testing.T) {
	api := newFake()
	api.addErr = &client.APIError{Status: 403, Code: client.CodeIAMAdminTarget}
	s := SchemaOf(t, NewMembership())
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&MembershipResource{api: api}).Create(Ctx, resource.CreateRequest{Plan: membershipPlan(t, "team_1", "usr_9")}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "Only an owner") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestMembershipImport(t *testing.T) {
	s := SchemaOf(t, NewMembership())
	for id, ok := range map[string]bool{"team_1/usr_1": true, "usr_1/team_1": false, "team_1": false, "team_1/usr_1/x": false} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&MembershipResource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("%s: diagnostics = %v", id, resp.Diagnostics)
		}
	}
}
