package instance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// fakeAPI is an in-memory instanceAPI. A non-nil *Err is returned by the
// matching method. createLands makes a failing create still store the instance,
// which simulates a lost response. opStuck makes an operation never finish on
// its own, so a wait only succeeds when its done check passes.
type fakeAPI struct {
	instances []client.Instance

	createErr, listErr, getErr, renameErr, deleteErr, orderErr, opErr, untilErr error
	createLands, opStuck, deleteHard                                            bool

	nextID      int
	creates     int
	renames     int
	deletes     int
	orderWaits  int
	untilWaits  int
	lists       int
	lastCreate  client.CreateInstanceRequest
	lastRenamed string
}

var errConnReset = errors.New("connection reset by peer")

func stamp() *time.Time {
	t := time.Date(2026, 10, 3, 23, 38, 52, 919071000, time.UTC)
	return &t
}

func ptr(s string) *string { return &s }

func running(id, name string) client.Instance {
	return client.Instance{
		ID: id, Name: name, PlanSlug: "individual", ImageSlug: "ubuntu-24-04", Region: "af-abj", Zone: "af-abj-1",
		SecurityGroupID: "sg_default", PrivateIPv4: ptr("102.211.122.77"), DesiredState: "running", ObservedState: "running",
		Tags: map[string]string{}, CreatedAt: stamp(), UpdatedAt: stamp(),
	}
}

func (f *fakeAPI) CreateInstance(_ context.Context, req client.CreateInstanceRequest) (*client.InstanceOrderReference, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil && !f.createLands {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("vm_%d", f.nextID)
	inst := running(id, req.Name)
	inst.PlanSlug, inst.ImageSlug = req.PlanSlug, req.ImageSlug
	inst.Tags = req.Tags
	if inst.Tags == nil {
		inst.Tags = map[string]string{}
	}
	f.instances = append(f.instances, inst)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &client.InstanceOrderReference{OrderID: fmt.Sprintf("ord_%d", f.nextID), InstanceID: id, Status: client.OrderAwaitingPayment, AmountMinor: 1504000, Currency: "NGN"}, nil
}

func (f *fakeAPI) GetInstance(_ context.Context, id string) (*client.Instance, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.instances {
		if f.instances[i].ID == id {
			inst := f.instances[i]
			return &inst, nil
		}
	}
	return nil, fmt.Errorf("getting instance: %w", client.ErrNotFound)
}

// ListInstances leaves out deleted instances, as the real list does.
func (f *fakeAPI) ListInstances(context.Context) ([]client.Instance, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var live []client.Instance
	for _, inst := range f.instances {
		if inst.ObservedState != client.InstanceDeleted {
			live = append(live, inst)
		}
	}
	return live, nil
}

func (f *fakeAPI) RenameInstance(_ context.Context, id, name string) (*client.OperationReference, error) {
	f.renames++
	f.lastRenamed = name
	if f.renameErr != nil {
		return nil, f.renameErr
	}
	for i := range f.instances {
		if f.instances[i].ID == id {
			f.instances[i].Name = name
		}
	}
	return &client.OperationReference{OperationID: "op_rename", ResourceID: id}, nil
}

// DeleteInstance marks the instance deleted but keeps it readable, as the real
// API does. deleteHard removes it, so a read returns 404.
func (f *fakeAPI) DeleteInstance(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.instances {
		if f.instances[i].ID == id {
			if f.deleteHard {
				f.instances = append(f.instances[:i], f.instances[i+1:]...)
			} else {
				f.instances[i].ObservedState, f.instances[i].DesiredState = client.InstanceDeleted, client.InstanceDeleted
			}
			break
		}
	}
	return &client.OperationReference{OperationID: "op_delete", ResourceID: id}, nil
}

func (f *fakeAPI) WaitForInstanceOrder(_ context.Context, id string) (*client.InstanceOrder, error) {
	f.orderWaits++
	if f.orderErr != nil {
		return nil, f.orderErr
	}
	return &client.InstanceOrder{ID: id, Status: client.OrderProvisioned}, nil
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.opErr != nil {
		return f.opErr
	}
	return f.settle(ctx, done)
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	f.untilWaits++
	if f.untilErr != nil {
		return f.untilErr
	}
	return f.settle(ctx, done)
}

// settle runs the done check once and fails if it does not pass and the
// operation is stuck, like a wait that can never finish.
func (f *fakeAPI) settle(ctx context.Context, done client.DoneCheck) error {
	if done != nil {
		reached, err := done(ctx)
		if err != nil {
			return err
		}
		if reached {
			return nil
		}
	}
	if f.opStuck {
		return errors.New("never finished and the done check did not pass")
	}
	return nil
}
