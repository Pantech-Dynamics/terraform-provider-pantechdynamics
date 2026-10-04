package resourcekit_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

func status(observed, desired string, err error) resourcekit.StatusGetter {
	return func(context.Context) (resourcekit.Status, error) {
		return resourcekit.Status{Observed: observed, Desired: desired}, err
	}
}

func TestIsActive(t *testing.T) {
	failedOp := func(context.Context, string) (*client.Operation, error) {
		return &client.Operation{ID: "op_1", Kind: "create_network", Status: client.OperationFailed,
			Failure: &client.OperationFailure{Code: "IP_ADDRESS_UNAVAILABLE", Reason: "unavailable"}}, nil
	}
	pendingOp := func(context.Context, string) (*client.Operation, error) {
		return &client.Operation{ID: "op_1", Status: client.OperationSubmitted}, nil
	}
	boom := errors.New("boom")

	tests := []struct {
		name      string
		get       resourcekit.StatusGetter
		getOp     resourcekit.OperationGetter
		wantDone  bool
		wantErr   string
		wantOpErr bool
	}{
		{"active", status("active", "present", nil), nil, true, "", false},
		{"still provisioning", status("provisioning", "present", nil), nil, false, "", false},
		{"not visible yet", status("", "", fmt.Errorf("get: %w", client.ErrNotFound)), nil, false, "", false},
		{"read error ends the wait", status("", "", boom), nil, false, "boom", false},
		{"failed reports the operation's code", status("failed", "present", nil), failedOp, false, "IP_ADDRESS_UNAVAILABLE", true},
		{"failed, operation not recorded yet", status("failed", "present", nil), pendingOp, false, "entered the \"failed\" state", false},
		{"failed, operation unreadable", status("failed", "present", nil), nil, false, "entered the \"failed\" state", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done, err := resourcekit.IsActive(tt.get, tt.getOp, "network", "net_1", "op_1")(context.Background())
			if done != tt.wantDone {
				t.Errorf("done = %v, want %v", done, tt.wantDone)
			}
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			var opErr *client.OperationError
			if errors.As(err, &opErr) != tt.wantOpErr {
				t.Errorf("is OperationError = %v, want %v", !tt.wantOpErr, tt.wantOpErr)
			}
		})
	}
}

func TestIsGone(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		get      resourcekit.StatusGetter
		wantDone bool
		wantErr  bool
	}{
		{"404", status("", "", client.ErrNotFound), true, false},
		{"deleted record", status("deleted", "deleted", nil), true, false},
		{"failed create with deleted intent", status("failed", "deleted", nil), true, false},
		{"failed but still wanted", status("failed", "present", nil), false, false},
		{"still deleting", status("deleting", "deleted", nil), false, false},
		{"still active", status("active", "present", nil), false, false},
		{"read error", status("", "", boom), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done, err := resourcekit.IsGone(tt.get)(context.Background())
			if done != tt.wantDone || (err != nil) != tt.wantErr {
				t.Errorf("done = %v, err = %v; want done %v, err %v", done, err, tt.wantDone, tt.wantErr)
			}
		})
	}
}
