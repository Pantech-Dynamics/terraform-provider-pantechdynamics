package databasesnapshot

import (
	"errors"
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
		"id": UnknownStr(), "database_id": Str("db_1"), "name": Str("pre-upgrade"),
		"observed_state": UnknownStr(), "trigger": UnknownStr(), "size_bytes": Unknown(tftypes.Number),
		"region": UnknownStr(), "completed_at": UnknownStr(), "created_at": UnknownStr(),
	})
}

// stateFor is the stored state of snap_1 of db_1.
func stateFor(s schema.Schema) tfsdk.State {
	return State(s, map[string]tftypes.Value{"id": Str("snap_1"), "database_id": Str("db_1"), "name": Str("pre-upgrade")})
}

func create(t *testing.T, api *fakeAPI) *resource.CreateResponse {
	t.Helper()
	s := testSchema(t)
	resp := &resource.CreateResponse{State: EmptyState(s)}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: planFor(s)}, resp)
	return resp
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}

func TestCreate(t *testing.T) {
	t.Run("takes the snapshot and waits until it is active", func(t *testing.T) {
		api := &fakeAPI{}
		resp := create(t, api)
		if resp.Diagnostics.HasError() {
			t.Fatal(ErrorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.ID.ValueString() != "snap_1" || m.DatabaseID.ValueString() != "db_1" || m.ObservedState.ValueString() != "active" || m.SizeBytes.ValueInt64() != 1<<30 {
			t.Fatalf("model = %+v", m)
		}
	})

	t.Run("a refused name shows the API's message and a hint", func(t *testing.T) {
		api := &fakeAPI{createErr: &client.APIError{Status: 409, Code: client.CodeSnapshotNameTaken, Detail: "This database already has a snapshot named pre-upgrade."}}
		text := ErrorText(create(t, api).Diagnostics)
		if !strings.Contains(text, "already has a snapshot named pre-upgrade") || !strings.Contains(text, "Choose another name") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a database that is not settled is refused with the API's message", func(t *testing.T) {
		api := &fakeAPI{createErr: &client.APIError{Status: 409, Code: "DATABASE_SNAPSHOT_NOT_READY", Detail: "The database is provisioning."}}
		text := ErrorText(create(t, api).Diagnostics)
		if !strings.Contains(text, "The database is provisioning.") || !strings.Contains(text, "running or stopped") {
			t.Fatalf("diags = %s", text)
		}
	})

	t.Run("a lost response adopts the snapshot instead of taking another", func(t *testing.T) {
		api := &fakeAPI{createErr: errors.New("connection reset by peer"), createLands: true}
		resp := create(t, api)
		if resp.Diagnostics.HasError() || api.creates != 1 || api.lists != 1 {
			t.Fatalf("diags = %s, creates = %d, lists = %d", ErrorText(resp.Diagnostics), api.creates, api.lists)
		}
		if getModel(t, resp.State).ID.ValueString() != "snap_1" {
			t.Fatal("the snapshot was not adopted")
		}
	})

	t.Run("a failed snapshot keeps its id in state and shows the failure", func(t *testing.T) {
		api := &fakeAPI{endState: client.SnapshotFailed, opFailure: &client.Operation{ID: "op_snap_1", Status: client.OperationFailed,
			Failure: &client.OperationFailure{Code: "SNAPSHOT_FAILED", Reason: "the disk could not be copied"}}}
		resp := create(t, api)
		if !strings.Contains(ErrorText(resp.Diagnostics), "the disk could not be copied") {
			t.Fatalf("diags = %s", ErrorText(resp.Diagnostics))
		}
		if getModel(t, resp.State).ID.ValueString() != "snap_1" {
			t.Fatal("the id must be saved so the failed snapshot is deleted on the next apply")
		}
	})
}

func TestRead(t *testing.T) {
	for _, tt := range []struct {
		name        string
		api         *fakeAPI
		wantRemoved bool
	}{
		{"present", &fakeAPI{snaps: []client.Snapshot{{ID: "snap_1", Name: "pre-upgrade", DatabaseID: ptr("db_1"), ObservedState: "active"}}}, false},
		{"its database is gone but it is not", &fakeAPI{orphaned: true, snaps: []client.Snapshot{{ID: "snap_1", Name: "pre-upgrade", DatabaseID: ptr("db_1"), ObservedState: "active"}}}, false},
		{"deleted but readable", &fakeAPI{snaps: []client.Snapshot{{ID: "snap_1", DatabaseID: ptr("db_1"), ObservedState: client.SnapshotDeleted}}}, true},
		{"gone", &fakeAPI{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := testSchema(t)
			resp := &resource.ReadResponse{State: stateFor(s)}
			(&Resource{api: tt.api}).Read(Ctx, resource.ReadRequest{State: stateFor(s)}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(ErrorText(resp.Diagnostics))
			}
			if IsRemoved(resp.State) != tt.wantRemoved {
				t.Fatalf("removed = %v", IsRemoved(resp.State))
			}
		})
	}
}

func TestDelete(t *testing.T) {
	t.Run("deletes and waits until it is gone", func(t *testing.T) {
		api := &fakeAPI{snaps: []client.Snapshot{{ID: "snap_1", DatabaseID: ptr("db_1"), ObservedState: "active"}}}
		s := testSchema(t)
		resp := &resource.DeleteResponse{State: stateFor(s)}
		(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
		if resp.Diagnostics.HasError() || api.deletes != 1 {
			t.Fatalf("diags = %s, deletes = %d", ErrorText(resp.Diagnostics), api.deletes)
		}
	})
	t.Run("already gone is success", func(t *testing.T) {
		s := testSchema(t)
		resp := &resource.DeleteResponse{State: stateFor(s)}
		(&Resource{api: &fakeAPI{}}).Delete(Ctx, resource.DeleteRequest{State: stateFor(s)}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(ErrorText(resp.Diagnostics))
		}
	})
}

func TestImportState(t *testing.T) {
	s := testSchema(t)
	for _, tt := range []struct {
		id    string
		valid bool
	}{
		{"db_1/snap_1", true},
		{"snap_1", false},
		{"db_1/vol_1", false},
		{"vm_1/snap_1", false},
	} {
		resp := &resource.ImportStateResponse{State: EmptyState(s)}
		(&Resource{}).ImportState(Ctx, resource.ImportStateRequest{ID: tt.id}, resp)
		if resp.Diagnostics.HasError() == tt.valid {
			t.Errorf("%s: diags = %s", tt.id, ErrorText(resp.Diagnostics))
			continue
		}
		if tt.valid {
			if m := getModel(t, resp.State); m.ID.ValueString() != "snap_1" || m.DatabaseID.ValueString() != "db_1" {
				t.Errorf("%s: model = %+v", tt.id, m)
			}
		}
	}
}

func TestSchemaReplacesOnEveryArgument(t *testing.T) {
	s := testSchema(t)
	for _, name := range []string{"database_id", "name"} {
		if !s.Attributes[name].IsRequired() {
			t.Errorf("%s must be required", name)
		}
	}
}
