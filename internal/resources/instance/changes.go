package instance

import (
	"context"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// changeSecurityGroup points the instance at another security group. The platform
// only accepts this on a stopped instance, so the instance is stopped first and
// left stopped: the caller decides afterwards whether it should run again.
func (r *Resource) changeSecurityGroup(ctx context.Context, id, securityGroupID string) error {
	if err := r.applyPower(ctx, id, client.InstanceStopped); err != nil {
		return err
	}
	ref, err := r.api.ChangeInstanceSecurityGroup(ctx, id, securityGroupID)
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.hasSecurityGroup(id, securityGroupID))
}

// resize moves the instance to a bigger plan. The platform only accepts this on a
// running instance, so a stopped one is started first. The platform then stops,
// resizes and restarts it, which took about 4.5 minutes, so the wait finishes on
// the new plan being in place and the instance running again, never on the
// operation alone.
func (r *Resource) resize(ctx context.Context, id, planSlug string) error {
	if err := r.applyPower(ctx, id, client.InstanceRunning); err != nil {
		return err
	}
	ref, err := r.api.ResizeInstance(ctx, id, planSlug)
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.hasPlan(id, planSlug))
}

// changeSSHKey gives the instance another SSH key. The platform only accepts
// this on a running instance, so a stopped one is started first. The platform
// then stops it, installs the key and starts it again. Nothing on the instance
// says which key it holds, so the wait is the operation's alone, which only
// succeeds once the instance is running again.
func (r *Resource) changeSSHKey(ctx context.Context, id, sshKeyID string) error {
	if err := r.applyPower(ctx, id, client.InstanceRunning); err != nil {
		return err
	}
	ref, err := r.api.ChangeInstanceSSHKey(ctx, id, sshKeyID)
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, nil)
}

// hasSecurityGroup finishes a group change when the instance reports the new group.
func (r *Resource) hasSecurityGroup(id, want string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		inst, err := r.api.GetInstance(ctx, id)
		if err != nil {
			return false, err
		}
		return inst.SecurityGroupID == want, nil
	}
}

// hasPlan finishes a resize when the new plan is in place and the instance has
// come back up. While it resizes the instance reports provisioning with the old
// plan, so both conditions are needed.
func (r *Resource) hasPlan(id, want string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		inst, err := r.api.GetInstance(ctx, id)
		if err != nil {
			return false, err
		}
		if inst.ObservedState == client.InstanceFailed {
			return false, failedInstanceError(inst)
		}
		return inst.PlanSlug == want && inst.ObservedState == client.InstanceRunning, nil
	}
}
