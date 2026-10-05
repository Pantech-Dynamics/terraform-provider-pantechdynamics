package resourcekit

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func TestFailureCodeHint(t *testing.T) {
	tests := []struct {
		code, want string
	}{
		{FailureCapacityUnavailable, "plan_slug"},
		{FailureLimitExceeded, "Delete resources you no longer use"},
		{FailureResourceBusy, "settle"},
		{FailureUnavailable, "in a few minutes"},
		{FailureInternalError, "Contact support"},
		{FailureGeneric, "quote the operation id"},
		{"PROVISIONING_CAPACITY_ERROR", "plan_slug"},          // legacy, mapped
		{"PROVISIONING_TIMEOUT", "in a few minutes"},          // legacy, mapped
		{"PROVISIONING_JOB_FAILED", "quote the operation id"}, // legacy, any other
	}
	for _, tt := range tests {
		if got := FailureCodeHint(tt.code); !strings.Contains(got, tt.want) {
			t.Errorf("FailureCodeHint(%q) = %q, want it to contain %q", tt.code, got, tt.want)
		}
	}
	for _, code := range []string{"", "INSTANCE_BUSY", "IP_POOL_EXHAUSTED"} {
		if got := FailureCodeHint(code); got != "" {
			t.Errorf("FailureCodeHint(%q) = %q, want none", code, got)
		}
	}
	for code := range failureHints {
		if FailureCodeHint(code) == "" {
			t.Errorf("%s has no hint", code)
		}
	}
}

func TestAddWaitErrorAddsTheFailureHint(t *testing.T) {
	opErr := &client.OperationError{Operation: client.Operation{ID: "op_1", Kind: "create_instance", Status: client.OperationFailed,
		Failure: &client.OperationFailure{Code: FailureCapacityUnavailable, Reason: "There is no capacity for this in the zone right now."}}}
	var diags diag.Diagnostics
	AddWaitError(&diags, "Error creating instance", "instance", "vm_1", fmt.Errorf("waiting: %w", opErr))
	detail := diags.Errors()[0].Detail()
	if !strings.Contains(detail, "PROVISIONING_CAPACITY_UNAVAILABLE") || !strings.Contains(detail, "plan_slug") {
		t.Fatalf("detail = %q", detail)
	}

	diags = nil
	AddWaitError(&diags, "Error", "instance", "vm_1", errors.New("boom"))
	if got := diags.Errors()[0].Detail(); got != "boom" {
		t.Fatalf("a plain error gets no hint, got %q", got)
	}
}
