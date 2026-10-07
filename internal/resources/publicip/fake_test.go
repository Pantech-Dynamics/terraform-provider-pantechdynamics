package publicip

import (
	"context"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory publicIPAPI. A non-nil *Err is returned by the matching method.
type fakeAPI struct {
	ips []client.PublicIP

	createErr, getErr, deleteErr, waitErr error
	attachErr, detachErr                  error
	createState                           string
	opStuck, keepOnDelete                 bool
	op                                    *client.Operation

	// syncAfter is how many reads an attach or detach stays in_sync false.
	syncAfter int
	// noOp makes attach and detach answer an empty operation_id.
	noOp bool

	nextID     int
	creates    int
	deletes    int
	attaches   []string // instance ids, in order
	detaches   int
	waitedOps  []string
	lastCreate client.CreatePublicIPRequest
	unsynced   int
}

func now() *time.Time {
	t := time.Date(2026, 10, 4, 15, 0, 0, 123000000, time.UTC)
	return &t
}

func ptr(s string) *string { return &s }

func (f *fakeAPI) CreatePublicIP(_ context.Context, req client.CreatePublicIPRequest) (*client.OperationReference, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("pip_%d", f.nextID)
	state := f.createState
	if state == "" {
		state = "active"
	}
	ip := client.PublicIP{
		ID: id, NetworkID: req.NetworkID, Purpose: req.Purpose, Address: ptr("203.0.113.9"), Region: ptr("af-abj"), Zone: ptr("af-abj-2"), NetworkName: ptr("main"),
		DesiredState: "present", ObservedState: state, InSync: true, CreatedAt: now(), UpdatedAt: now(),
	}
	if req.InstanceID != "" {
		ip.InstanceID = ptr(req.InstanceID)
	}
	f.ips = append(f.ips, ip)
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id}, nil
}

func (f *fakeAPI) GetPublicIP(_ context.Context, id string) (*client.PublicIP, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.ips {
		if f.ips[i].ID == id {
			if !f.ips[i].InSync {
				if f.unsynced <= 0 {
					f.ips[i].InSync = true
				}
				f.unsynced--
			}
			ip := f.ips[i]
			return &ip, nil
		}
	}
	return nil, fmt.Errorf("getting public ip: %w", client.ErrNotFound)
}

func (f *fakeAPI) find(id string) *client.PublicIP {
	for i := range f.ips {
		if f.ips[i].ID == id {
			return &f.ips[i]
		}
	}
	return nil
}

func (f *fakeAPI) AttachPublicIP(_ context.Context, id, instanceID string) (*client.OperationReference, error) {
	f.attaches = append(f.attaches, instanceID)
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	ip := f.find(id)
	if ip == nil {
		return nil, fmt.Errorf("attaching: %w", client.ErrNotFound)
	}
	return f.moved(ip, ptr(instanceID)), nil
}

func (f *fakeAPI) DetachPublicIP(_ context.Context, id string) (*client.OperationReference, error) {
	f.detaches++
	if f.detachErr != nil {
		return nil, f.detachErr
	}
	ip := f.find(id)
	if ip == nil {
		return nil, fmt.Errorf("detaching: %w", client.ErrNotFound)
	}
	return f.moved(ip, nil), nil
}

// moved points ip at instance and leaves it out of sync for syncAfter reads.
func (f *fakeAPI) moved(ip *client.PublicIP, instance *string) *client.OperationReference {
	ip.InstanceID, ip.InstanceName = instance, nil
	if f.noOp {
		return &client.OperationReference{ResourceID: ip.ID, Status: client.OperationSucceeded}
	}
	ip.InSync = false
	f.unsynced = f.syncAfter
	return &client.OperationReference{OperationID: "op_move", ResourceID: ip.ID}
}

// WaitUntil polls done a bounded number of times, like the client's loop
// without the sleep.
func (f *fakeAPI) WaitUntil(ctx context.Context, what string, done client.DoneCheck) error {
	for range 20 {
		reached, err := done(ctx)
		if err != nil || reached {
			return err
		}
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

func (f *fakeAPI) DeletePublicIP(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.ips {
		if f.ips[i].ID == id && !f.keepOnDelete {
			f.ips = append(f.ips[:i], f.ips[i+1:]...)
			break
		}
	}
	return &client.OperationReference{OperationID: "op_del", ResourceID: id}, nil
}

func (f *fakeAPI) GetOperation(context.Context, string) (*client.Operation, error) {
	if f.op == nil {
		return nil, fmt.Errorf("getting operation: %w", client.ErrNotFound)
	}
	return f.op, nil
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error {
	f.waitedOps = append(f.waitedOps, id)
	if f.waitErr != nil {
		return f.waitErr
	}
	return kittest.Wait(ctx, done, f.opStuck)
}
