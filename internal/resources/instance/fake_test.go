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

	createErr, listErr, getErr, renameErr, deleteErr, orderErr, opErr, untilErr, startErr, stopErr, resizeErr, groupErr, attachErr error
	createLands, opStuck, deleteHard                                                                                               bool

	nextID      int
	creates     int
	renames     int
	starts      int
	resizes     int
	groupSwaps  int
	refused     int // changes sent to an instance in the wrong state, which the platform rejects with 409
	stops       int
	redundant   int // actions sent to an instance already in the target state
	settleTo    string
	deletes     int
	orderWaits  int
	untilWaits  int
	lists       int
	lastCreate  client.CreateInstanceRequest
	lastRenamed string
	attaches    int
	detaches    int
	netOrder    []string // attach, detach and group changes, in the order sent
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

// StopInstance behaves like the real backend: stopping an instance that is not
// running fails the operation and leaves the instance failed, which the
// redundant counter records so a test can prove it never happened.
func (f *fakeAPI) StopInstance(_ context.Context, id string) (*client.OperationReference, error) {
	f.stops++
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	f.act(id, client.InstanceRunning, client.InstanceStopped)
	return &client.OperationReference{OperationID: "op_stop", ResourceID: id}, nil
}

// StartInstance is the mirror of StopInstance.
func (f *fakeAPI) StartInstance(_ context.Context, id string) (*client.OperationReference, error) {
	f.starts++
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.act(id, client.InstanceStopped, client.InstanceRunning)
	return &client.OperationReference{OperationID: "op_start", ResourceID: id}, nil
}

// act moves an instance from one settled state to the other, or fails it when it
// was not in the expected starting state.
func (f *fakeAPI) act(id, from, to string) {
	for i := range f.instances {
		if f.instances[i].ID != id {
			continue
		}
		if f.instances[i].ObservedState != from {
			f.redundant++
			f.instances[i].ObservedState = client.InstanceFailed
			f.instances[i].Failure = &client.InstanceFailure{Code: "PROVISIONING_RETRIES_EXHAUSTED", Reason: "invalid instance state transition"}
			return
		}
		f.instances[i].ObservedState, f.instances[i].DesiredState = to, to
	}
}

// planRank orders the plans the way the platform does, smallest first.
var planRank = map[string]int{"individual": 1, "starter": 2, "developer": 3, "performance": 4, "business": 5}

// ResizeInstance behaves like the real backend: the instance must be running and
// the plan must be bigger, and a rejected call changes nothing.
func (f *fakeAPI) ResizeInstance(_ context.Context, id, planSlug string) (*client.OperationReference, error) {
	f.resizes++
	if f.resizeErr != nil {
		return nil, f.resizeErr
	}
	for i := range f.instances {
		if f.instances[i].ID != id {
			continue
		}
		if f.instances[i].ObservedState != client.InstanceRunning {
			f.refused++
			return nil, &client.APIError{Status: 409, Code: client.CodeInvalidResourceState}
		}
		if planRank[planSlug] <= planRank[f.instances[i].PlanSlug] {
			return nil, &client.APIError{Status: 422, Code: "VALIDATION_FAILED",
				Errors: []client.FieldError{{Field: "plan_slug", Code: client.FieldCodePlanNotBigger, Message: "must be a bigger plan"}}}
		}
		f.instances[i].PlanSlug = planSlug
	}
	return &client.OperationReference{OperationID: "op_resize", ResourceID: id}, nil
}

// ChangeInstanceSecurityGroup behaves like the real backend: the instance must be stopped.
func (f *fakeAPI) ChangeInstanceSecurityGroup(_ context.Context, id, securityGroupID string) (*client.OperationReference, error) {
	f.groupSwaps++
	f.netOrder = append(f.netOrder, "group")
	if f.groupErr != nil {
		return nil, f.groupErr
	}
	for i := range f.instances {
		if f.instances[i].ID != id {
			continue
		}
		if f.instances[i].ObservedState != client.InstanceStopped {
			f.refused++
			return nil, &client.APIError{Status: 409, Code: client.CodeInstanceMustBeStopped}
		}
		f.instances[i].SecurityGroupID = securityGroupID
	}
	return &client.OperationReference{OperationID: "op_group", ResourceID: id}, nil
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
	if f.settleTo != "" { // a transition finishes while we wait
		for i := range f.instances {
			f.instances[i].ObservedState = f.settleTo
			f.instances[i].DesiredState = f.settleTo
		}
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

// AttachInstancePrivateNetwork gives the instance an interface and an address.
func (f *fakeAPI) AttachInstancePrivateNetwork(_ context.Context, id string) (*client.OperationReference, error) {
	f.attaches++
	f.netOrder = append(f.netOrder, "attach")
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	for i := range f.instances {
		if f.instances[i].ID == id {
			f.instances[i].PrivateNetworkState = client.PrivateNetworkAttached
			f.instances[i].PrivateNetworkIP = ptr("10.250.0.7")
		}
	}
	return &client.OperationReference{OperationID: "op_attach", ResourceID: id}, nil
}

// DetachInstancePrivateNetwork removes the interface and its address.
func (f *fakeAPI) DetachInstancePrivateNetwork(_ context.Context, id string) (*client.OperationReference, error) {
	f.detaches++
	f.netOrder = append(f.netOrder, "detach")
	for i := range f.instances {
		if f.instances[i].ID == id {
			f.instances[i].PrivateNetworkState = client.PrivateNetworkNone
			f.instances[i].PrivateNetworkIP = nil
		}
	}
	return &client.OperationReference{OperationID: "op_detach", ResourceID: id}, nil
}
