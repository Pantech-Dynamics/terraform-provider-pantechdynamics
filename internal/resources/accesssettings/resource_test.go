package accesssettings

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

type fakeAPI struct {
	s      client.AccessSettings
	setErr error
	sets   int
}

func (f *fakeAPI) GetAccessSettings(context.Context) (*client.AccessSettings, error) {
	s := f.s
	return &s, nil
}

func (f *fakeAPI) SetAccessSettings(_ context.Context, b string) (*client.AccessSettings, error) {
	f.sets++
	if f.setErr != nil {
		return nil, f.setErr
	}
	f.s.MemberBaseline, f.s.Version = b, f.s.Version+1
	return f.GetAccessSettings(context.Background())
}

func TestSettingsLifecycle(t *testing.T) {
	api := &fakeAPI{s: client.AccessSettings{MemberBaseline: "read", Version: 1}}
	s := SchemaOf(t, New())
	r := &Resource{api: api}
	plan := Plan(s, map[string]tftypes.Value{"id": UnknownStr(), "member_baseline": Str("none"), "version": Unknown(tftypes.Number), "updated_at": UnknownStr()})

	cresp := &resource.CreateResponse{State: EmptyState(s)}
	r.Create(Ctx, resource.CreateRequest{Plan: plan}, cresp)
	var m model
	cresp.State.Get(Ctx, &m)
	if cresp.Diagnostics.HasError() || m.MemberBaseline.ValueString() != "none" || m.ID.ValueString() != "access_settings" || m.Version.ValueInt64() != 2 {
		t.Fatalf("diagnostics = %v, model = %+v", cresp.Diagnostics, m)
	}

	// Destroy leaves the setting as it is, with a warning, and calls nothing.
	dresp := &resource.DeleteResponse{State: cresp.State}
	r.Delete(Ctx, resource.DeleteRequest{State: cresp.State}, dresp)
	if dresp.Diagnostics.HasError() || dresp.Diagnostics.WarningsCount() != 1 || api.sets != 1 || api.s.MemberBaseline != "none" {
		t.Fatalf("diagnostics = %v, sets = %d", dresp.Diagnostics, api.sets)
	}
	if !strings.Contains(dresp.Diagnostics.Warnings()[0].Detail(), `stays "none"`) {
		t.Fatalf("warning = %s", dresp.Diagnostics.Warnings()[0].Detail())
	}

	api.setErr = &client.APIError{Status: 403, Code: "FORBIDDEN"}
	cresp = &resource.CreateResponse{State: EmptyState(s)}
	r.Create(Ctx, resource.CreateRequest{Plan: plan}, cresp)
	if !strings.Contains(ErrorText(cresp.Diagnostics), "Only owners") {
		t.Fatalf("diagnostics = %q", ErrorText(cresp.Diagnostics))
	}

	iresp := &resource.ImportStateResponse{State: EmptyState(s)}
	r.ImportState(Ctx, resource.ImportStateRequest{ID: "anything"}, iresp)
	rresp := &resource.ReadResponse{State: iresp.State}
	r.Read(Ctx, resource.ReadRequest{State: iresp.State}, rresp)
	rresp.State.Get(Ctx, &m)
	if m.ID.ValueString() != "access_settings" || m.MemberBaseline.ValueString() != "none" {
		t.Fatalf("model = %+v", m)
	}
}
