package subnet

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

func planFor(s schema.Schema) tfsdk.Plan {
	return Plan(s, map[string]tftypes.Value{
		"id": UnknownStr(), "network_id": Str("net_1"), "name": Str("web"), "cidr": Str("10.0.1.0/24"),
		"region": UnknownStr(), "zone": UnknownStr(), "observed_state": UnknownStr(), "created_at": UnknownStr(), "updated_at": UnknownStr(),
	})
}

func stateFor(s schema.Schema) tfsdk.State {
	return State(s, map[string]tftypes.Value{
		"id": Str("snet_1"), "network_id": Str("net_1"), "name": Str("web"), "cidr": Str("10.0.1.0/24"),
		"region": Str("af-abj"), "zone": Str("af-abj-2"), "observed_state": Str("active"),
		"created_at": Str("2026-10-04T15:00:00Z"), "updated_at": Str("2026-10-04T15:00:00Z"),
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

func create(t *testing.T, api *fakeAPI) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s)}, resp)
	return resp
}

func TestCreateSavesTheSubnetInItsNetwork(t *testing.T) {
	api := &fakeAPI{}
	resp := create(t, api)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	m := getModel(t, resp.State)
	if m.ID.ValueString() != "snet_1" || m.NetworkID.ValueString() != "net_1" || m.Zone.ValueString() != "af-abj-2" || m.ObservedState.ValueString() != "active" {
		t.Errorf("model = %+v", m)
	}
	if api.lastNetworkID != "net_1" || api.lastCreate.CIDR != "10.0.1.0/24" {
		t.Errorf("sent network %q, request %+v", api.lastNetworkID, api.lastCreate)
	}
}

// A subnet whose network never came up fails with a code. It must still be
// saved by id so a later destroy cleans it up.
func TestCreateFailureKeepsTheIDAndReportsTheCode(t *testing.T) {
	api := &fakeAPI{
		createState: "failed",
		op:          &client.Operation{ID: "op_snet_1", Kind: "create_subnet", Status: client.OperationFailed, Failure: &client.OperationFailure{Code: "PROVISIONING_RETRIES_EXHAUSTED", Reason: "network has no provider binding yet"}},
	}
	resp := create(t, api)
	if !strings.Contains(ErrorText(resp.Diagnostics), "no provider binding") {
		t.Fatalf("diagnostics = %q, want the failure reason", ErrorText(resp.Diagnostics))
	}
	if IsRemoved(resp.State) || getModel(t, resp.State).ID.ValueString() != "snet_1" {
		t.Error("the id was not saved, so the failed subnet would be orphaned")
	}
}

func TestCreateRefusedByTheAPIPointsAtTheAttribute(t *testing.T) {
	api := &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "cidr", Code: "INVALID_CIDR", Message: "bad range"}}}}
	resp := create(t, api)
	if !strings.Contains(ErrorText(resp.Diagnostics), "INVALID_CIDR") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %v, removed = %v", resp.Diagnostics, IsRemoved(resp.State))
	}
}

func TestReadRemovesAMissingSubnet(t *testing.T) {
	s := testSchema(t)
	for name, api := range map[string]*fakeAPI{
		"404":     {},
		"deleted": {subnets: []client.Subnet{{ID: "snet_1", ObservedState: "deleted"}}},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.ReadResponse{State: stateFor(s)}
			(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
			if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
				t.Errorf("want the subnet removed from state, diags %v", resp.Diagnostics)
			}
		})
	}
}

func TestDeleteWaitsForTheSubnetToBeGone(t *testing.T) {
	s := testSchema(t)
	for name, api := range map[string]*fakeAPI{
		"removed":            {subnets: []client.Subnet{{ID: "snet_1", ObservedState: "active"}}, opStuck: true},
		"failed record kept": {subnets: []client.Subnet{{ID: "snet_1", DesiredState: "deleted", ObservedState: "failed"}}, opStuck: true, keepOnDelete: true},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &resource.DeleteResponse{State: stateFor(s)}
			(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
		})
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
		{"still in use", &client.APIError{Status: 409, Code: client.CodeInvalidResourceState, Detail: "subnet has instances"}, "instances are still in it"},
		{"clusters in it", &client.APIError{Status: 409, Code: client.CodeSubnetHasKubernetesClusters, Detail: "subnet has clusters"}, "Kubernetes clusters still run in this subnet"},
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
	for id, wantErr := range map[string]bool{"snet_abc": false, "net_abc": true} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("id %q: error = %v, want %v", id, resp.Diagnostics.HasError(), wantErr)
		}
	}
}
