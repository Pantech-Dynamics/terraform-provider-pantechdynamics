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

// pollCheck is one look at a resource. It reports whether the wait is over, the
// status it saw (for error messages), or an error that ends the wait.
type pollCheck func(ctx context.Context) (done bool, status string, err error)

// pollUntil is the one polling loop. It runs check, and if the wait is not over
// it sleeps one poll interval, until check ends the wait or ctx does. what names
// the thing waited for, in error messages.
func (c *Client) pollUntil(ctx context.Context, what string, check pollCheck) error {
	for {
		done, status, err := check(ctx)
		if err != nil || done {
			return err
		}
		if err := c.retry.sleep(ctx, c.pollInterval); err != nil {
			return fmt.Errorf("waiting for %s (last status %q): %w", what, status, err)
		}
	}
}

// WaitForOperation polls until the operation succeeds, done reports true, the
// operation fails, or ctx ends. done may be nil. It exists because the
// operation record is not always updated: on dev, a security group delete stayed
// "submitted" for over an hour although the group was long gone. The resource is
// the authority, and the operation is trusted to report failure. A failed
// operation is returned as an *OperationError so callers can read the failure
// code. Callers set the deadline through ctx.
func (c *Client) WaitForOperation(ctx context.Context, id string, done DoneCheck) error {
	return c.pollUntil(ctx, "operation "+id, func(ctx context.Context) (bool, string, error) {
		if done != nil {
			reached, err := done(ctx)
			if err != nil {
				return false, "", fmt.Errorf("checking whether operation %s reached its goal: %w", id, err)
			}
			if reached {
				return true, "", nil
			}
		}

		op, err := c.GetOperation(ctx, id)
		if err != nil {
			return false, "", err
		}
		switch op.Status {
		case OperationSucceeded:
			return true, op.Status, nil
		case OperationFailed:
			return false, op.Status, &OperationError{Operation: *op}
		}
		return false, op.Status, nil
	})
}

// WaitUntil polls until done reports true, or ctx ends. It waits on a resource
// alone, with no operation to follow, for example to see an instance reach
// running. what names the thing waited for, in errors. A done error ends the wait.
func (c *Client) WaitUntil(ctx context.Context, what string, done DoneCheck) error {
	return c.pollUntil(ctx, what, func(ctx context.Context) (bool, string, error) {
		reached, err := done(ctx)
		return reached, "", err
	})
}
