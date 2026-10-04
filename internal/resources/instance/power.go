package instance

import (
	"context"
	"fmt"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// applyPower brings the instance to the target state, "running" or "stopped".
//
// The platform fails a redundant action: stopping a stopped instance, or starting
// a running one, returns 202 and then fails the operation after about a minute,
// leaving the instance in the failed state. So this reads the instance first and
// sends an action only when it is in the opposite settled state. It never retries
// a stop, and it refuses to touch an instance that is already failed.
func (r *Resource) applyPower(ctx context.Context, id, target string) error {
	inst, err := r.settled(ctx, id)
	if err != nil {
		return err
	}

	switch {
	case inst.ObservedState == target:
		return nil // already there: sending anything would only break it
	case inst.ObservedState == client.InstanceFailed:
		return failedInstanceError(inst)
	case inst.ObservedState != client.InstanceRunning && inst.ObservedState != client.InstanceStopped:
		return fmt.Errorf("instance %s is %q, which cannot be changed to %q", id, inst.ObservedState, target)
	}

	var ref *client.OperationReference
	if target == client.InstanceStopped {
		ref, err = r.api.StopInstance(ctx, id)
	} else {
		ref, err = r.api.StartInstance(ctx, id)
	}
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.hasObservedState(id, target))
}

// settled reads the instance, first waiting out a transition (starting up,
// stopping), so the next decision is made on a settled state.
func (r *Resource) settled(ctx context.Context, id string) (*client.Instance, error) {
	inst, err := r.api.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isTransitional(inst.ObservedState) {
		return inst, nil
	}
	if inst.ObservedState == client.InstanceDeleting {
		return nil, fmt.Errorf("instance %s is being deleted", id)
	}

	err = r.api.WaitUntil(ctx, "instance "+id+" to settle", func(ctx context.Context) (bool, error) {
		current, err := r.api.GetInstance(ctx, id)
		if err != nil {
			return false, err
		}
		inst = current
		return !isTransitional(current.ObservedState), nil
	})
	if err != nil {
		return nil, err
	}
	return inst, nil
}

func isTransitional(state string) bool {
	switch state {
	case client.InstancePending, client.InstanceProvisioning, client.InstanceStopping, client.InstanceDeleting:
		return true
	}
	return false
}

// hasObservedState finishes a power change when the instance reaches the state.
// A failed instance ends the wait at once, with the reason the platform gave.
func (r *Resource) hasObservedState(id, want string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		inst, err := r.api.GetInstance(ctx, id)
		if err != nil {
			return false, err
		}
		if inst.ObservedState == client.InstanceFailed {
			return false, failedInstanceError(inst)
		}
		return inst.ObservedState == want, nil
	}
}

// failedInstanceError explains a failed instance. The platform's reason can hold
// internal provider text, so it is shown but never interpreted.
func failedInstanceError(inst *client.Instance) error {
	return fmt.Errorf("instance %s is in the failed state%s. Start and stop do not reliably recover a failed instance, so nothing was sent. Delete it and create it again, for example with `terraform apply -replace=<address>`", inst.ID, failureText(inst.Failure))
}
