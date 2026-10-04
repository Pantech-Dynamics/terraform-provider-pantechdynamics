package instance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func instanceIn(state string) client.Instance {
	inst := running("vm_1", "web")
	inst.ObservedState = state
	inst.DesiredState = state
	return inst
}

// The platform fails a redundant start or stop and leaves the instance failed, so
// every case here also asserts that no redundant action was ever sent.
func TestApplyPower(t *testing.T) {
	failedInst := instanceIn(client.InstanceFailed)
	failedInst.Failure = &client.InstanceFailure{Code: "PROVISIONING_RETRIES_EXHAUSTED", Reason: "the provider request failed"}

	tests := []struct {
		name        string
		inst        client.Instance
		target      string
		api         func(*fakeAPI)
		wantStops   int
		wantStarts  int
		wantErrText string
		wantState   string
	}{
		{"stop a running instance", instanceIn("running"), "stopped", nil, 1, 0, "", "stopped"},
		{"start a stopped instance", instanceIn("stopped"), "running", nil, 0, 1, "", "running"},
		{"already running: nothing is sent", instanceIn("running"), "running", nil, 0, 0, "", "running"},
		{"already stopped: nothing is sent", instanceIn("stopped"), "stopped", nil, 0, 0, "", "stopped"},
		{"a failed instance is refused without sending anything", failedInst, "running", nil, 0, 0, "failed state", "failed"},
		{"a failed instance is refused for a stop too", failedInst, "stopped", nil, 0, 0, "the provider request failed", "failed"},
		{"stopping settles to stopped: a stop target needs no action", instanceIn("stopping"), "stopped", func(f *fakeAPI) { f.settleTo = "stopped" }, 0, 0, "", "stopped"},
		{"stopping settles to stopped: a run target starts it", instanceIn("stopping"), "running", func(f *fakeAPI) { f.settleTo = "stopped" }, 0, 1, "", "running"},
		{"provisioning settles to running: a run target needs no action", instanceIn("provisioning"), "running", func(f *fakeAPI) { f.settleTo = "running" }, 0, 0, "", "running"},
		{"an instance being deleted is refused", instanceIn("deleting"), "running", nil, 0, 0, "being deleted", "deleting"},
		{"an unknown state is refused", instanceIn("unknown"), "stopped", nil, 0, 0, "cannot be changed", "unknown"},
		{"a stop error is returned", instanceIn("running"), "stopped", func(f *fakeAPI) { f.stopErr = &client.APIError{Status: 409, Code: client.CodeInvalidResourceState} }, 1, 0, "INVALID_RESOURCE_STATE", "running"},
		{"a start error is returned", instanceIn("stopped"), "running", func(f *fakeAPI) { f.startErr = errConnReset }, 0, 1, "connection reset", "stopped"},
		{"a failed operation is returned", instanceIn("running"), "stopped", func(f *fakeAPI) {
			f.opErr = &client.OperationError{Operation: client.Operation{ID: "op_stop", Kind: "stop_instance", Status: "failed", Failure: &client.OperationFailure{Code: "BOOM", Reason: "x"}}}
		}, 1, 0, "BOOM", "stopped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := seeded(tt.inst)
			if tt.api != nil {
				tt.api(api)
			}

			err := newTestResource(api).applyPower(context.Background(), "vm_1", tt.target)

			if api.stops != tt.wantStops || api.starts != tt.wantStarts {
				t.Fatalf("stops = %d, starts = %d, want %d and %d", api.stops, api.starts, tt.wantStops, tt.wantStarts)
			}
			if api.redundant != 0 {
				t.Fatalf("%d redundant action(s) were sent: the platform would have failed the instance", api.redundant)
			}
			if tt.wantErrText == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErrText != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErrText)) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErrText)
			}
			if got := api.instances[0].ObservedState; got != tt.wantState {
				t.Fatalf("observed state = %q, want %q", got, tt.wantState)
			}
		})
	}
}

func TestApplyPowerLookupFailure(t *testing.T) {
	api := seeded()
	if err := newTestResource(api).applyPower(context.Background(), "vm_1", "stopped"); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if api.stops != 0 || api.starts != 0 {
		t.Fatal("nothing may be sent for an instance that cannot be read")
	}
}

func TestFailedInstanceErrorQuotesTheReasonAndGivesAWayOut(t *testing.T) {
	inst := instanceIn("failed")
	inst.Failure = &client.InstanceFailure{Code: "PROVISIONING_RETRIES_EXHAUSTED", Reason: "the provider request failed"}
	text := failedInstanceError(&inst).Error()
	for _, want := range []string{"vm_1", "PROVISIONING_RETRIES_EXHAUSTED", "the provider request failed", "-replace"} {
		if !strings.Contains(text, want) {
			t.Errorf("error %q does not contain %q", text, want)
		}
	}
}

func TestDesiredStateValidator(t *testing.T) {
	v := oneOf{allowed: []string{"running", "stopped"}}
	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{"running", types.StringValue("running"), false},
		{"stopped", types.StringValue("stopped"), false},
		{"deleted is not allowed", types.StringValue("deleted"), true},
		{"empty", types.StringValue(""), true},
		{"wrong case", types.StringValue("Running"), true},
		{"null", types.StringNull(), false},
		{"unknown", types.StringUnknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{ConfigValue: tt.value}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %v", resp.Diagnostics)
			}
		})
	}
}

func TestUpdatePower(t *testing.T) {
	s := testSchema(t)
	// stateDesired is what the refreshed state holds for desired_state.
	updateFrom := func(api *fakeAPI, stateDesired, planName, planDesired string) resource.UpdateResponse {
		prior := stateFor(s)
		prior.Raw = withAttr(t, s, prior.Raw, "desired_state", str(stateDesired))
		resp := resource.UpdateResponse{State: prior}
		newTestResource(api).Update(ctx, resource.UpdateRequest{
			Plan:  planFor(s, "vm_1", planName, map[string]tftypes.Value{"ssh_key_id": str("sshk_1"), "desired_state": str(planDesired)}),
			State: prior,
		}, &resp)
		return resp
	}
	update := func(api *fakeAPI, planName, planDesired string) resource.UpdateResponse {
		return updateFrom(api, "running", planName, planDesired)
	}

	t.Run("stops a running instance in place", func(t *testing.T) {
		api := seeded(instanceIn("running"))
		resp := update(api, "web", "stopped")
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if m.DesiredState.ValueString() != "stopped" || m.ObservedState.ValueString() != "stopped" {
			t.Fatalf("state = %+v", m)
		}
		if api.stops != 1 || api.creates != 0 || api.deletes != 0 || api.redundant != 0 {
			t.Fatalf("stops = %d, creates = %d, deletes = %d, redundant = %d", api.stops, api.creates, api.deletes, api.redundant)
		}
	})

	t.Run("starts a stopped instance in place", func(t *testing.T) {
		api := seeded(instanceIn("stopped"))
		// stopped outside Terraform: the refresh put "stopped" in state, the config says running
		resp := updateFrom(api, "stopped", "web", "running")
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.starts != 1 || getModel(t, resp.State).ObservedState.ValueString() != "running" {
			t.Fatalf("starts = %d", api.starts)
		}
	})

	t.Run("an instance already in the target state sends nothing", func(t *testing.T) {
		api := seeded(instanceIn("stopped"))
		resp := update(api, "web", "stopped")
		if resp.Diagnostics.HasError() || api.stops != 0 || api.starts != 0 || api.redundant != 0 {
			t.Fatalf("stops = %d, starts = %d, diags = %v", api.stops, api.starts, resp.Diagnostics)
		}
	})

	t.Run("rename and power change together", func(t *testing.T) {
		api := seeded(instanceIn("running"))
		resp := update(api, "web-new", "stopped")
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if api.renames != 1 || api.stops != 1 || m.Name.ValueString() != "web-new" || m.DesiredState.ValueString() != "stopped" {
			t.Fatalf("renames = %d, stops = %d, state = %+v", api.renames, api.stops, m)
		}
	})

	t.Run("a failed power change keeps the rename that already happened", func(t *testing.T) {
		api := seeded(instanceIn("running"))
		api.stopErr = &client.APIError{Status: 409, Code: client.CodeInvalidResourceState}

		resp := update(api, "web-new", "stopped")

		if !resp.Diagnostics.HasError() || !strings.Contains(errorText(resp.Diagnostics), "power state") {
			t.Fatalf("diags = %v", resp.Diagnostics)
		}
		if got := getModel(t, resp.State).Name.ValueString(); got != "web-new" {
			t.Fatalf("name in state = %q: the rename succeeded and must not be lost", got)
		}
	})

	t.Run("a failed instance is refused with guidance", func(t *testing.T) {
		failed := instanceIn("failed")
		failed.Failure = &client.InstanceFailure{Code: "PROVISIONING_RETRIES_EXHAUSTED", Reason: "x"}
		api := seeded(failed)
		text := errorText(update(api, "web", "stopped").Diagnostics)
		if !strings.Contains(text, "failed state") || !strings.Contains(text, "-replace") || api.stops != 0 {
			t.Fatalf("diags = %s, stops = %d", text, api.stops)
		}
	})
}

// withAttr returns the object value with one attribute replaced.
func withAttr(t *testing.T, s schema.Schema, raw tftypes.Value, name string, v tftypes.Value) tftypes.Value {
	t.Helper()
	var attrs map[string]tftypes.Value
	if err := raw.As(&attrs); err != nil {
		t.Fatal(err)
	}
	attrs[name] = v
	return tftypes.NewValue(objectType(s), attrs)
}

func TestCreateWithRequestedPowerState(t *testing.T) {
	s := testSchema(t)
	create := func(api *fakeAPI, desired string) resource.CreateResponse {
		resp := resource.CreateResponse{State: emptyState(s)}
		newTestResource(api).Create(ctx, resource.CreateRequest{Plan: planFor(s, "", "web", map[string]tftypes.Value{
			"ssh_key_id": str("sshk_1"), "desired_state": str(desired),
		})}, &resp)
		return resp
	}

	t.Run("running is the default and sends no power action", func(t *testing.T) {
		api := seeded()
		resp := create(api, "running")
		if resp.Diagnostics.HasError() || api.stops != 0 || api.starts != 0 {
			t.Fatalf("stops = %d, starts = %d, diags = %v", api.stops, api.starts, resp.Diagnostics)
		}
		if getModel(t, resp.State).DesiredState.ValueString() != "running" {
			t.Fatal("state must hold running")
		}
	})

	t.Run("stopped runs first and then stops it", func(t *testing.T) {
		api := seeded()
		resp := create(api, "stopped")
		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		m := getModel(t, resp.State)
		if api.creates != 1 || api.stops != 1 || m.DesiredState.ValueString() != "stopped" || m.ObservedState.ValueString() != "stopped" || api.redundant != 0 {
			t.Fatalf("creates = %d, stops = %d, state = %+v", api.creates, api.stops, m)
		}
	})

	t.Run("a failed stop after create keeps the instance in state", func(t *testing.T) {
		api := seeded()
		api.stopErr = errConnReset
		resp := create(api, "stopped")
		if !strings.Contains(errorText(resp.Diagnostics), "stopping the new instance") || getModel(t, resp.State).ID.ValueString() != "vm_1" {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})
}
