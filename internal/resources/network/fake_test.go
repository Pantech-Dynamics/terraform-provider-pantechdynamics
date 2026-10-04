package network

import (
	"context"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory networkAPI. A non-nil *Err is returned by the matching
// method. createState is the observed state a new network starts in.
type fakeAPI struct {
	networks []client.Network

	createErr, getErr, deleteErr, waitErr error
	createState                           string
	opStuck, keepOnDelete                 bool
	op                                    *client.Operation

	nextID     int
	creates    int
	deletes    int
	lastCreate client.CreateNetworkRequest
}

func now() *time.Time {
	t := time.Date(2026, 10, 4, 15, 0, 0, 123000000, time.UTC)
	return &t
}

func (f *fakeAPI) CreateNetwork(_ context.Context, req client.CreateNetworkRequest) (*client.OperationReference, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("net_%d", f.nextID)
	state := f.createState
	if state == "" {
		state = "active"
	}
	f.networks = append(f.networks, client.Network{
		ID: id, Name: req.Name, CIDR: req.CIDR, Region: "af-abj", Zone: "af-abj-2",
		DesiredState: "present", ObservedState: state, CreatedAt: now(), UpdatedAt: now(),
	})
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id}, nil
}

func (f *fakeAPI) GetNetwork(_ context.Context, id string) (*client.Network, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.networks {
		if f.networks[i].ID == id {
			n := f.networks[i]
			return &n, nil
		}
	}
	return nil, fmt.Errorf("getting network: %w", client.ErrNotFound)
}

func (f *fakeAPI) DeleteNetwork(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.networks {
		if f.networks[i].ID == id && !f.keepOnDelete {
			f.networks = append(f.networks[:i], f.networks[i+1:]...)
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
