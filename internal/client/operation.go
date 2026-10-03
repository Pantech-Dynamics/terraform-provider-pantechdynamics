package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Operation statuses. Succeeded and failed are terminal.
const (
	OperationSubmitting = "submitting"
	OperationSubmitted  = "submitted"
	OperationSucceeded  = "succeeded"
	OperationFailed     = "failed"
)

// defaultPollInterval is how long WaitForOperation sleeps between checks. The
// docs say polling every 2 to 5 seconds is plenty.
const defaultPollInterval = 2 * time.Second

// OperationReference is the 202 body returned by every asynchronous write. It
// names the operation to poll and the resource the write affects.
type OperationReference struct {
	OperationID string `json:"operation_id"`
	ResourceID  string `json:"resource_id"`
	Status      string `json:"status"`
}

// OperationFailure explains why an operation failed.
type OperationFailure struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Operation is the state of an asynchronous write.
type Operation struct {
	ID           string            `json:"id"`
	ResourceType string            `json:"resource_type"`
	ResourceID   string            `json:"resource_id"`
	Kind         string            `json:"kind"`
	Status       string            `json:"status"`
	Failure      *OperationFailure `json:"failure"`
	CreatedAt    *time.Time        `json:"created_at"`
	UpdatedAt    *time.Time        `json:"updated_at"`
}

// OperationError is returned when an operation ends in the failed state.
type OperationError struct {
	Operation Operation
}

// Error carries the stable failure code and the operation id, so a user can
// quote them to support.
func (e *OperationError) Error() string {
	msg := fmt.Sprintf("operation %s (%s) failed", e.Operation.ID, e.Operation.Kind)
	if f := e.Operation.Failure; f != nil {
		msg += fmt.Sprintf(": %s: %s", f.Code, f.Reason)
	}
	return msg
}

// GetOperation returns the current state of an operation. It returns
// ErrNotFound if the operation record is gone.
func (c *Client) GetOperation(ctx context.Context, id string) (*Operation, error) {
	var op Operation
	if err := c.do(ctx, http.MethodGet, "/operations/"+url.PathEscape(id), nil, &op); err != nil {
		return nil, fmt.Errorf("getting operation %s: %w", id, err)
	}
	return &op, nil
}

// DoneCheck reports whether a resource has reached the goal state of an
// operation. WaitForOperation accepts one as a second way to finish.
type DoneCheck func(ctx context.Context) (bool, error)

// WaitForOperation polls until the operation succeeds, done reports true, the
// operation fails, or ctx ends. It is the one place polling lives. done may be
// nil. It exists because the operation record is not always updated: on dev, a
// security group delete stayed "submitted" for over an hour although the group
// was long gone. The resource is the authority, and the operation is trusted to
// report failure. A failed operation is returned as an *OperationError so callers
// can read the failure code. Callers set the deadline through ctx.
func (c *Client) WaitForOperation(ctx context.Context, id string, done DoneCheck) error {
	for {
		if done != nil {
			reached, err := done(ctx)
			if err != nil {
				return fmt.Errorf("checking whether operation %s reached its goal: %w", id, err)
			}
			if reached {
				return nil
			}
		}

		op, err := c.GetOperation(ctx, id)
		if err != nil {
			return err
		}
		switch op.Status {
		case OperationSucceeded:
			return nil
		case OperationFailed:
			return &OperationError{Operation: *op}
		}

		if err := c.retry.sleep(ctx, c.pollInterval); err != nil {
			return fmt.Errorf("waiting for operation %s (last status %q): %w", id, op.Status, err)
		}
	}
}
