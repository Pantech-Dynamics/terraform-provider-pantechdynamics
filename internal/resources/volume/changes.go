package volume

import (
	"context"
	"errors"
	"fmt"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// attach attaches the volume to an instance and waits until the platform reports
// it attached. If the attach fails, the volume is left asking for an instance it
// is not on, and the platform then refuses to delete it, so the stale intent is
// cleared before the error is returned.
func (r *Resource) attach(ctx context.Context, id, instanceID string) error {
	ref, err := r.api.AttachVolume(ctx, id, instanceID)
	if err == nil {
		err = r.api.WaitForOperation(ctx, ref.OperationID, r.isAttachedTo(id, instanceID))
	}
	if err == nil {
		return nil
	}
	if !isContextError(err) {
		r.clearAttachIntent(ctx, id)
	}
	return err
}

// detach detaches an attached volume and waits until it is no longer attached.
// It sends nothing when the volume is not attached: detaching one that is not
// fails the operation after about a minute.
func (r *Resource) detach(ctx context.Context, id string) error {
	vol, err := r.api.GetVolume(ctx, id)
	if err != nil {
		return err
	}
	if vol.AttachedInstanceID == nil {
		return nil
	}
	ref, err := r.api.DetachVolume(ctx, id)
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.isDetached(id))
}

// clearAttachIntent resets a volume that was asked to attach but did not. The
// platform only forgets the request when a detach is sent, and that detach is
// expected to fail, so the result is ignored. Without it the volume cannot be
// deleted (409).
func (r *Resource) clearAttachIntent(ctx context.Context, id string) {
	ref, err := r.api.DetachVolume(ctx, id)
	if err != nil {
		return
	}
	var opErr *client.OperationError
	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.isIdle(id)); err != nil && !errors.As(err, &opErr) {
		return
	}
}

// resize grows the volume to the offering, and to the size for a customized
// offering. It waits for the new size to be in place, never on the operation alone.
func (r *Resource) resize(ctx context.Context, id, offering string, sizeGB, target int64) error {
	ref, err := r.api.ResizeVolume(ctx, id, offering, sizeGB)
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.hasSize(id, target))
}

// isAttachedTo finishes an attach when the volume reports the instance. A volume
// asked to attach whose operation failed is reported by the operation itself.
func (r *Resource) isAttachedTo(id, instanceID string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		vol, err := r.api.GetVolume(ctx, id)
		if err != nil {
			return false, err
		}
		return vol.AttachedInstanceID != nil && *vol.AttachedInstanceID == instanceID, nil
	}
}

// isDetached finishes a detach when the volume is attached to nothing.
func (r *Resource) isDetached(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		vol, err := r.api.GetVolume(ctx, id)
		if err != nil {
			return false, err
		}
		return vol.AttachedInstanceID == nil, nil
	}
}

// isIdle finishes when the volume has neither an attachment nor a pending request.
func (r *Resource) isIdle(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		vol, err := r.api.GetVolume(ctx, id)
		if err != nil {
			return false, err
		}
		return vol.AttachedInstanceID == nil && vol.DesiredInstanceID == nil, nil
	}
}

// hasSize finishes a resize when the volume has the new size and is active.
func (r *Resource) hasSize(id string, want int64) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		vol, err := r.api.GetVolume(ctx, id)
		if err != nil {
			return false, err
		}
		if vol.ObservedState == client.VolumeFailed {
			return false, fmt.Errorf("volume %s is in the failed state", id)
		}
		return vol.SizeGB == want && vol.ObservedState == client.VolumeActive, nil
	}
}

// isActive finishes a create when the volume is active. A failed volume ends the
// wait at once.
func (r *Resource) isActive(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		vol, err := r.api.GetVolume(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		if vol.ObservedState == client.VolumeFailed {
			return false, fmt.Errorf("volume %s is in the failed state", id)
		}
		return vol.ObservedState == client.VolumeActive, nil
	}
}

// isGone finishes a delete when the volume reads as deleted or no longer exists.
func (r *Resource) isGone(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		vol, err := r.api.GetVolume(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return vol.ObservedState == client.VolumeDeleted, nil
	}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
