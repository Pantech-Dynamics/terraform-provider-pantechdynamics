package loadbalancer

import (
	"context"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory lbAPI. A non-nil *Err is returned by the matching method.
type fakeAPI struct {
	lbs []client.LoadBalancer

	createErr, getErr, updateErr, deleteErr, waitErr error
	createState                                      string
	outOfSync                                        bool // the load balancer never reports in_sync
	noOpUpdate                                       bool // the update returns an empty operation id
	opStuck                                          bool
	op                                               *client.Operation

	nextID     int
	updates    int
	lastCreate client.CreateLoadBalancerRequest
	lastUpdate client.UpdateLoadBalancerRequest
}

func now() *time.Time {
	t := time.Date(2026, 10, 7, 10, 0, 0, 123000000, time.UTC)
	return &t
}

func ptr(s string) *string { return &s }

func members(ids []string) []client.LoadBalancerMember {
	out := []client.LoadBalancerMember{}
	for _, id := range ids {
		out = append(out, client.LoadBalancerMember{InstanceID: id, DesiredState: "present", ObservedState: "active"})
	}
	return out
}

func (f *fakeAPI) CreateLoadBalancer(_ context.Context, req client.CreateLoadBalancerRequest) (*client.OperationReference, error) {
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("lb_%d", f.nextID)
	state := f.createState
	if state == "" {
		state = "active"
	}
	// The backend defaults the algorithm, the private port and the allow-list.
	lb := client.LoadBalancer{
		ID: id, Name: req.Name, PublicIPID: req.PublicIPID, PublicIPAddress: ptr("203.0.113.9"), NetworkID: ptr("net_1"),
		SubnetID: req.SubnetID, Protocol: "tcp", Algorithm: req.Algorithm, PublicPort: req.PublicPort, PrivatePort: req.PublicPort,
		CIDRList: req.CIDRList, Members: members(req.InstanceIDs), DesiredState: "present", ObservedState: state,
		InSync: !f.outOfSync, CreatedAt: now(), UpdatedAt: now(),
	}
	if lb.Algorithm == "" {
		lb.Algorithm = "roundrobin"
	}
	if req.PrivatePort != nil {
		lb.PrivatePort = *req.PrivatePort
	}
	f.lbs = append(f.lbs, lb)
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id}, nil
}

func (f *fakeAPI) GetLoadBalancer(_ context.Context, id string) (*client.LoadBalancer, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.lbs {
		if f.lbs[i].ID == id {
			lb := f.lbs[i]
			return &lb, nil
		}
	}
	return nil, fmt.Errorf("getting load balancer: %w", client.ErrNotFound)
}

func (f *fakeAPI) UpdateLoadBalancer(_ context.Context, id string, req client.UpdateLoadBalancerRequest) (*client.OperationReference, error) {
	f.updates++
	f.lastUpdate = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.noOpUpdate {
		return &client.OperationReference{ResourceID: id}, nil
	}
	for i := range f.lbs {
		if f.lbs[i].ID != id {
			continue
		}
		lb := &f.lbs[i]
		if req.Name != nil {
			lb.Name = *req.Name
		}
		if req.Algorithm != nil {
			lb.Algorithm = *req.Algorithm
		}
		if req.InstanceIDs != nil {
			lb.Members = members(*req.InstanceIDs)
		}
		lb.InSync = !f.outOfSync
	}
	return &client.OperationReference{OperationID: "op_upd", ResourceID: id}, nil
}

func (f *fakeAPI) DeleteLoadBalancer(_ context.Context, id string) (*client.OperationReference, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.lbs {
		if f.lbs[i].ID == id {
			f.lbs = append(f.lbs[:i], f.lbs[i+1:]...)
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
