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
	createState                           string
	opStuck, keepOnDelete                 bool
	op                                    *client.Operation

	nextID     int
	creates    int
	deletes    int
	lastCreate client.CreatePublicIPRequest
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
		ID: id, NetworkID: req.NetworkID, Purpose: req.Purpose, Address: ptr("203.0.113.9"), Region: ptr("af-abj"), ZoneID: ptr("af-abj-2"),
		DesiredState: "present", ObservedState: state, CreatedAt: now(), UpdatedAt: now(),
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
			ip := f.ips[i]
			return &ip, nil
		}
	}
	return nil, fmt.Errorf("getting public ip: %w", client.ErrNotFound)
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

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.waitErr != nil {
		return f.waitErr
	}
	return kittest.Wait(ctx, done, f.opStuck)
}
