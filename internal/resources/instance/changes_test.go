package instance

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// instanceWith builds a settled instance, in the default security group, with the
// given power state and plan.
func instanceWith(state, plan string) client.Instance {
	inst := running("vm_1", "web")
	inst.ObservedState, inst.DesiredState, inst.PlanSlug = state, state, plan
	return inst
}

// updateWith runs Update from a stored state (name web, plan individual, group
// sg_default, and the given desired_state) to a plan that changes what is given.
func updateWith(t *testing.T, api *fakeAPI, stateDesired string, change map[string]tftypes.Value) resource.UpdateResponse {
	t.Helper()
	return updateFrom(t, api, stateDesired, nil, change)
}

// updateFrom is updateWith with extra attributes overridden in the stored state.
func updateFrom(t *testing.T, api *fakeAPI, stateDesired string, stored, change map[string]tftypes.Value) resource.UpdateResponse {
	t.Helper()
	s := testSchema(t)
	prior := stateFor(s)
	prior.Raw = withAttr(t, s, prior.Raw, "desired_state", str(stateDesired))
	for name, v := range stored {
		prior.Raw = withAttr(t, s, prior.Raw, name, v)
	}

	set := map[string]tftypes.Value{
		"ssh_key_id": str("sshk_1"), "desired_state": str(stateDesired),
		"plan_slug": str("individual"), "security_group_id": str("sg_default"),
	}
	for k, v := range change {
		set[k] = v
	}
	resp := resource.UpdateResponse{State: prior}
	newTestResource(api).Update(ctx, resource.UpdateRequest{Plan: planFor(s, "vm_1", "web", set), State: prior}, &resp)
	return resp
}

func attachedTo(d diag.Diagnostics, p path.Path) bool {
	for _, e := range d.Errors() {
		if withPath, ok := e.(diag.DiagnosticWithPath); ok && withPath.Path().Equal(p) {
			return true
		}
	}
	return false
}

// Every case asserts refused == 0: a change sent to an instance in the wrong
// state is rejected by the platform, so a correct provider never sends one.
func TestResizeInPlace(t *testing.T) {
	t.Run("a running instance is resized and nothing else is touched", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		resp := updateWith(t, api, "running", map[string]tftypes.Value{"plan_slug": str("starter")})

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.resizes != 1 || api.starts != 0 || api.stops != 0 || api.refused != 0 || api.creates != 0 || api.deletes != 0 {
			t.Fatalf("resizes = %d, starts = %d, stops = %d, refused = %d", api.resizes, api.starts, api.stops, api.refused)
		}
		if got := getModel(t, resp.State).PlanSlug.ValueString(); got != "starter" {
			t.Fatalf("plan_slug in state = %q", got)
		}
	})

	t.Run("a stopped instance is started for the resize and stopped again", func(t *testing.T) {
		api := seeded(instanceWith("stopped", "individual"))
		resp := updateWith(t, api, "stopped", map[string]tftypes.Value{"plan_slug": str("starter")})

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.starts != 1 || api.resizes != 1 || api.stops != 1 || api.refused != 0 || api.redundant != 0 {
			t.Fatalf("starts = %d, resizes = %d, stops = %d, refused = %d", api.starts, api.resizes, api.stops, api.refused)
		}
		if got := getModel(t, resp.State).ObservedState.ValueString(); got != "stopped" {
			t.Fatalf("observed_state = %q: the instance must end where the configuration asks", got)
		}
	})

	t.Run("a smaller plan is refused with a clear message on plan_slug", func(t *testing.T) {
		api := seeded(instanceWith("running", "developer"))
		// the stored state holds developer, and the configuration now asks for individual
		resp := updateFrom(t, api, "running", map[string]tftypes.Value{"plan_slug": str("developer")},
			map[string]tftypes.Value{"plan_slug": str("individual")})

		text := errorText(resp.Diagnostics)
		if !strings.Contains(text, "only be resized to a bigger plan") || !strings.Contains(text, "-replace") {
			t.Fatalf("diags = %s", text)
		}
		if !attachedTo(resp.Diagnostics, path.Root("plan_slug")) {
			t.Fatal("the error must point at plan_slug")
		}
		if got := getModel(t, resp.State).PlanSlug.ValueString(); got != "developer" {
			t.Fatalf("state plan_slug = %q: state must reflect the real plan after a refusal", got)
		}
	})

	t.Run("a failed instance is refused and no resize is sent", func(t *testing.T) {
		failed := instanceWith("failed", "individual")
		failed.Failure = &client.InstanceFailure{Code: "PROVISIONING_RETRIES_EXHAUSTED", Reason: "x"}
		api := seeded(failed)

		resp := updateWith(t, api, "running", map[string]tftypes.Value{"plan_slug": str("starter")})

		if !strings.Contains(errorText(resp.Diagnostics), "failed state") || api.resizes != 0 || api.starts != 0 {
			t.Fatalf("diags = %s, resizes = %d, starts = %d", errorText(resp.Diagnostics), api.resizes, api.starts)
		}
	})

	t.Run("a stuck operation finishes when the new plan is in place and the instance runs", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		api.opStuck = true
		if resp := updateWith(t, api, "running", map[string]tftypes.Value{"plan_slug": str("starter")}); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})

	t.Run("an instance still resizing is not done until it runs again", func(t *testing.T) {
		// During a resize the instance reports provisioning, so the done check must wait.
		inst := instanceWith("provisioning", "starter")
		api := seeded(inst)
		done, err := newTestResource(api).hasPlan("vm_1", "starter")(ctx)
		if err != nil || done {
			t.Fatalf("done = %v, err = %v: the new plan alone is not enough", done, err)
		}
		api.instances[0].ObservedState = "running"
		if done, _ := newTestResource(api).hasPlan("vm_1", "starter")(ctx); !done {
			t.Fatal("the resize is done once the plan is in place and the instance runs")
		}
	})
}

func TestSecurityGroupChangeInPlace(t *testing.T) {
	group := map[string]tftypes.Value{"security_group_id": str("sg_new")}

	t.Run("a running instance is stopped, switched and started again", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		resp := updateWith(t, api, "running", group)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.stops != 1 || api.groupSwaps != 1 || api.starts != 1 || api.refused != 0 || api.redundant != 0 {
			t.Fatalf("stops = %d, swaps = %d, starts = %d, refused = %d", api.stops, api.groupSwaps, api.starts, api.refused)
		}
		m := getModel(t, resp.State)
		if m.SecurityGroupID.ValueString() != "sg_new" || m.ObservedState.ValueString() != "running" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("a stopped instance is switched and left stopped", func(t *testing.T) {
		api := seeded(instanceWith("stopped", "individual"))
		resp := updateWith(t, api, "stopped", group)

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.stops != 0 || api.starts != 0 || api.groupSwaps != 1 || api.refused != 0 {
			t.Fatalf("stops = %d, starts = %d, swaps = %d: a stopped instance needs no restart", api.stops, api.starts, api.groupSwaps)
		}
	})

	t.Run("a group and a plan change share one stop and one start", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		resp := updateWith(t, api, "running", map[string]tftypes.Value{"security_group_id": str("sg_new"), "plan_slug": str("starter")})

		if resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
		if api.stops != 1 || api.starts != 1 || api.groupSwaps != 1 || api.resizes != 1 || api.refused != 0 || api.redundant != 0 {
			t.Fatalf("stops = %d, starts = %d, swaps = %d, resizes = %d: the resize's start must double as the restart", api.stops, api.starts, api.groupSwaps, api.resizes)
		}
		m := getModel(t, resp.State)
		if m.SecurityGroupID.ValueString() != "sg_new" || m.PlanSlug.ValueString() != "starter" || m.ObservedState.ValueString() != "running" {
			t.Fatalf("state = %+v", m)
		}
	})

	t.Run("an unknown group in the plan is not a change", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		resp := updateWith(t, api, "running", map[string]tftypes.Value{"security_group_id": unknown()})
		if resp.Diagnostics.HasError() || api.groupSwaps != 0 || api.stops != 0 {
			t.Fatalf("swaps = %d, stops = %d, diags = %v", api.groupSwaps, api.stops, resp.Diagnostics)
		}
	})

	t.Run("an unknown group id points at security_group_id", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		api.groupErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED",
			Errors: []client.FieldError{{Field: "security_group_id", Code: "SECURITY_GROUP_NOT_FOUND", Message: "must identify a security group"}}}

		resp := updateWith(t, api, "running", group)

		if !attachedTo(resp.Diagnostics, path.Root("security_group_id")) {
			t.Fatalf("diags = %s", errorText(resp.Diagnostics))
		}
	})

	t.Run("a failed switch leaves the instance stopped and says so in state", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		api.groupErr = errConnReset

		resp := updateWith(t, api, "running", group)

		if !resp.Diagnostics.HasError() || api.starts != 0 {
			t.Fatalf("diags = %v, starts = %d", resp.Diagnostics, api.starts)
		}
		if got := getModel(t, resp.State).ObservedState.ValueString(); got != "stopped" {
			t.Fatalf("observed_state = %q: state must show the instance is stopped so the next apply starts it", got)
		}
	})

	t.Run("a stuck operation finishes when the instance reports the new group", func(t *testing.T) {
		api := seeded(instanceWith("running", "individual"))
		api.opStuck = true
		if resp := updateWith(t, api, "running", group); resp.Diagnostics.HasError() {
			t.Fatal(errorText(resp.Diagnostics))
		}
	})
}

func TestUpdateWithNothingToDoSendsNothing(t *testing.T) {
	api := seeded(instanceWith("running", "individual"))
	resp := updateWith(t, api, "running", nil)
	if resp.Diagnostics.HasError() {
		t.Fatal(errorText(resp.Diagnostics))
	}
	if api.stops+api.starts+api.resizes+api.groupSwaps+api.renames != 0 {
		t.Fatalf("stops = %d, starts = %d, resizes = %d, swaps = %d, renames = %d", api.stops, api.starts, api.resizes, api.groupSwaps, api.renames)
	}
}

func TestRenameAndGroupChangeTogether(t *testing.T) {
	s := testSchema(t)
	api := seeded(instanceWith("running", "individual"))
	resp := resource.UpdateResponse{State: stateFor(s)}

	newTestResource(api).Update(ctx, resource.UpdateRequest{
		Plan: planFor(s, "vm_1", "web-new", map[string]tftypes.Value{
			"ssh_key_id": str("sshk_1"), "security_group_id": str("sg_new"), "plan_slug": str("individual"),
		}),
		State: stateFor(s),
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatal(errorText(resp.Diagnostics))
	}
	m := getModel(t, resp.State)
	if api.renames != 1 || api.groupSwaps != 1 || m.Name.ValueString() != "web-new" || m.SecurityGroupID.ValueString() != "sg_new" || api.refused != 0 {
		t.Fatalf("renames = %d, swaps = %d, state = %+v", api.renames, api.groupSwaps, m)
	}
}
