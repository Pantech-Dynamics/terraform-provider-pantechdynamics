package subnet

import (
	"context"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory subnetAPI. A non-nil *Err is returned by the matching
// method. createState is the observed state a new subnet starts in.
type fakeAPI struct {
	subnets []client.Subnet

	createErr, getErr, deleteErr, waitErr error
	createState                           string
	opStuck, keepOnDelete                 bool
	op                                    *client.Operation

	nextID        int
	creates       int
	deletes       int
	lastNetworkID string
	lastCreate    client.CreateSubnetRequest
}

func now() *time.Time {
	t := time.Date(2026, 10, 4, 15, 0, 0, 123000000, time.UTC)
	return &t
}

func (f *fakeAPI) CreateSubnet(_ context.Context, networkID string, req client.CreateSubnetRequest) (*client.OperationReference, error) {
	f.creates++
	f.lastNetworkID, f.lastCreate = networkID, req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("snet_%d", f.nextID)
	state := f.createState
	if state == "" {
		state = "active"
	}
	f.subnets = append(f.subnets, client.Subnet{
		ID: id, NetworkID: networkID, Name: req.Name, CIDR: req.CIDR, Region: "af-abj", Zone: "af-abj-2",
		DesiredState: "present", ObservedState: state, CreatedAt: now(), UpdatedAt: now(),
	})
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id}, nil
}

func (f *fakeAPI) GetSubnet(_ context.Context, id string) (*client.Subnet, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.subnets {
		if f.subnets[i].ID == id {
			s := f.subnets[i]
			return &s, nil
		}
	}
	return nil, fmt.Errorf("getting subnet: %w", client.ErrNotFound)
}

func (f *fakeAPI) DeleteSubnet(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.subnets {
		if f.subnets[i].ID == id && !f.keepOnDelete {
			f.subnets = append(f.subnets[:i], f.subnets[i+1:]...)
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
