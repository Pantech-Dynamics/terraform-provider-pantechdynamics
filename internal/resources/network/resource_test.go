package network

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

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func planFor(s schema.Schema, region tftypes.Value) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "name": Str("main"), "cidr": Str("10.0.0.0/16"), "region": region, "zone": UnknownStr(),
		"observed_state": UnknownStr(), "created_at": UnknownStr(), "updated_at": UnknownStr(),
	})
}

func stateFor(s schema.Schema) tfsdk.State {
	return State(s, map[string]tftypes.Value{
		"id": Str("net_1"), "name": Str("main"), "cidr": Str("10.0.0.0/16"), "region": Str("af-abj"), "zone": Str("af-abj-2"),
		"observed_state": Str("active"), "created_at": Str("2026-10-04T15:00:00Z"), "updated_at": Str("2026-10-04T15:00:00Z"),
	})
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI, region tftypes.Value) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s, region)}, resp)
	return resp
}

func TestCreateSavesTheNetwork(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api, tftypes.NewValue(tftypes.String, nil))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "net_1" || m.Zone.ValueString() != "af-abj-2" || m.Region.ValueString() != "af-abj" || m.ObservedState.ValueString() != "active" {
		t.Errorf("model = %+v", m)
	}
	if m.CreatedAt.ValueString() != "2026-10-04T15:00:00Z" {
		t.Errorf("created_at = %s, want whole seconds", m.CreatedAt.ValueString())
	}
	if api.lastCreate.Region != "" {
		t.Errorf("region sent = %q, want it omitted", api.lastCreate.Region)
	}
}

func TestCreateSendsAConfiguredRegion(t *testing.T) {
	api := &fakeAPI{}
	if resp := create(t, api, Str("af-abj")); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.lastCreate.Region != "af-abj" {
		t.Errorf("region sent = %q", api.lastCreate.Region)
	}
}

// A network that fails to provision, as every staging network did, must still be
// saved by id so a later destroy cleans it up, and must report the failure code.
func TestCreateFailureKeepsTheIDAndReportsTheCode(t *testing.T) {
	api := &fakeAPI{
		createState: "failed",
		op:          &client.Operation{ID: "op_net_1", Kind: "create_network", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "IP_ADDRESS_UNAVAILABLE", Reason: "The reserved address is unavailable."}},
	}
	resp := create(t, api, tftypes.NewValue(tftypes.String, nil))
	text := ErrorText(resp.Diagnostics)
	if !resp.Diagnostics.HasError() || !strings.Contains(text, "IP_ADDRESS_UNAVAILABLE") {
		t.Fatalf("diagnostics = %q, want the failure code", text)
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "net_1" {
		t.Error("the id was not saved, so the failed network would be orphaned")
	}
}

func TestCreateRefusedByTheAPIPointsAtTheAttribute(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "region", Code: "REGION_NOT_AVAILABLE", Message: "not available"}}}}
	resp := create(t, api, Str("nowhere"))
	if !resp.Diagnostics.HasError() || !strings.Contains(ErrorText(resp.Diagnostics), "REGION_NOT_AVAILABLE") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if !IsRemoved(resp.State) {
		t.Error("nothing was created, so no state should be saved")
	}
}

func TestReadRemovesAMissingNetwork(t *testing.T) {
	s := testSchema(t)
	for name, api := range map[string]*fakeAPI{
		"404":     {},
		"deleted": {networks: []client.Network{{ID: "net_1", ObservedState: "deleted"}}},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ReadResponse{State: stateFor(s)}
			(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
			if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
				t.Errorf("want the network removed from state, diags %v", resp.Diagnostics)
			}
		})
	}
}

func TestDeleteWaitsForTheNetworkToBeGone(t *testing.T) {
	s := testSchema(t)
	api := &fakeAPI{networks: []client.Network{{ID: "net_1", ObservedState: "active"}}, opStuck: true}
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(api.networks) != 0 {
		t.Error("network still present")
	}
}

func TestDeleteTreatsAFailedNetworkWithDeletedIntentAsGone(t *testing.T) {
	s := testSchema(t)
	// The record a failed network keeps after its delete, as seen on staging.
	api := &fakeAPI{opStuck: true, keepOnDelete: true}
	api.networks = []client.Network{{ID: "net_1", DesiredState: "deleted", ObservedState: "failed"}}
	resp := &resource.DeleteResponse{State: stateFor(s)}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestDeleteIsIdempotentAndExplainsRefusals(t *testing.T) {
	s := testSchema(t)
	tests := []struct {
		name    string
		err     error
		wantErr string
	}{
		{"already gone", client.ErrNotFound, ""},
		{"still in use", &client.APIError{Status: 409, Code: client.CodeInvalidResourceState, Detail: "network has instances"}, "instances or public IPs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &resource.DeleteResponse{State: stateFor(s)}
			(&Resource{api: &fakeAPI{deleteErr: tt.err}}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
			if tt.wantErr == "" && resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if tt.wantErr != "" && !strings.Contains(ErrorText(resp.Diagnostics), tt.wantErr) {
				t.Errorf("diagnostics = %q, want %q", ErrorText(resp.Diagnostics), tt.wantErr)
			}
		})
	}
}

func TestImportRejectsAWrongPrefix(t *testing.T) {
	s := testSchema(t)
	for id, wantErr := range map[string]bool{"net_abc": false, "vm_abc": true} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("id %q: error = %v, want %v", id, resp.Diagnostics.HasError(), wantErr)
		}
	}
}

func TestUpdateOnlyStoresTheTimeouts(t *testing.T) {
	s := testSchema(t)
	api := &fakeAPI{}
	resp := &resource.UpdateResponse{State: stateFor(s)}
	plan := Plan(s, map[string]tftypes.Value{
		"id": Str("net_1"), "name": Str("main"), "cidr": Str("10.0.0.0/16"), "region": Str("af-abj"), "zone": Str("af-abj-2"),
		"observed_state": Str("active"), "created_at": Str("2026-10-04T15:00:00Z"), "updated_at": Str("2026-10-04T15:00:00Z"),
	})
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: plan, State: stateFor(s)}, resp)
	if resp.Diagnostics.HasError() || api.creates+api.deletes != 0 {
		t.Errorf("diags %v, creates %d, deletes %d", resp.Diagnostics, api.creates, api.deletes)
	}
}
