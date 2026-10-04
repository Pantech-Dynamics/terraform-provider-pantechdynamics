package resourcekit

import (
	"context"
	"errors"
	"fmt"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// Status is what a done check needs from a resource.
type Status struct {
	Observed string
	Desired  string
}

// StatusGetter reads a resource's status. It returns client.ErrNotFound, wrapped
// or not, when the resource does not exist.
type StatusGetter func(ctx context.Context) (Status, error)

// OperationGetter reads an operation, to explain a failed resource.
type OperationGetter func(ctx context.Context, id string) (*client.Operation, error)

const desiredDeleted = "deleted"

// IsActive finishes a create when the resource is active. The resource, not the
// operation record, is the authority, because operation records can lag. A
// failed resource ends the wait with the operation's failure code and reason
// (for example IP_ADDRESS_UNAVAILABLE), which the resource state alone lacks.
func IsActive(get StatusGetter, getOp OperationGetter, kind, id, opID string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		st, err := get(ctx)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		if st.Observed == StateFailed {
			return false, failedError(ctx, getOp, kind, id, opID)
		}
		return st.Observed == StateActive, nil
	}
}

// IsGone finishes a delete when the resource no longer exists. A resource that
// failed to create stays "failed" after its delete, so failed with a deleted
// intent counts as gone. The delete operation is not trusted for completion:
// on dev it has stayed "submitted" long after the resource was gone.
func IsGone(get StatusGetter) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		st, err := get(ctx)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return st.Observed == StateDeleted || (st.Observed == StateFailed && st.Desired == desiredDeleted), nil
	}
}

// failedError prefers the operation's own failure, which carries the code, and
// falls back to a plain message when the operation cannot be read or has not
// recorded the failure yet.
func failedError(ctx context.Context, getOp OperationGetter, kind, id, opID string) error {
	if getOp != nil && opID != "" {
		if op, err := getOp(ctx, opID); err == nil && op.Status == client.OperationFailed {
			return &client.OperationError{Operation: *op}
		}
	}
	return fmt.Errorf("%s %s entered the %q state", kind, id, StateFailed)
}
