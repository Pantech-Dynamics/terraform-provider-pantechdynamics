package portforward

import (
	"context"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory ruleAPI. A non-nil *Err is returned by the matching method.
type fakeAPI struct {
	rules []client.PortForwardingRule

	createErr, getErr, deleteErr, waitErr error
	createState                           string
	opStuck                               bool
	op                                    *client.Operation

	nextID     int
	creates    int
	lastIPID   string
	lastCreate client.CreatePortForwardingRuleRequest
}

func now() *time.Time {
	t := time.Date(2026, 10, 4, 15, 0, 0, 123000000, time.UTC)
	return &t
}

func (f *fakeAPI) CreatePortForwardingRule(_ context.Context, ipID string, req client.CreatePortForwardingRuleRequest) (*client.OperationReference, error) {
	f.creates++
	f.lastIPID, f.lastCreate = ipID, req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("pfr_%d", f.nextID)
	state := f.createState
	if state == "" {
		state = "active"
	}
	// The backend defaults the ends and the private start from the public start.
	rule := client.PortForwardingRule{
		ID: id, PublicIPID: ipID, InstanceID: req.InstanceID, Protocol: req.Protocol,
		PublicPortStart: req.PublicPortStart, PublicPortEnd: req.PublicPortStart,
		DesiredState: "present", ObservedState: state, CreatedAt: now(), UpdatedAt: now(),
	}
	if req.PublicPortEnd != nil {
		rule.PublicPortEnd = *req.PublicPortEnd
	}
	rule.PrivatePortStart = req.PublicPortStart
	if req.PrivatePortStart != nil {
		rule.PrivatePortStart = *req.PrivatePortStart
	}
	rule.PrivatePortEnd = rule.PrivatePortStart + (rule.PublicPortEnd - rule.PublicPortStart)
	if req.PrivatePortEnd != nil {
		rule.PrivatePortEnd = *req.PrivatePortEnd
	}
	f.rules = append(f.rules, rule)
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id}, nil
}

func (f *fakeAPI) GetPortForwardingRule(_ context.Context, ipID, id string) (*client.PortForwardingRule, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.rules {
		if f.rules[i].ID == id && f.rules[i].PublicIPID == ipID {
			r := f.rules[i]
			return &r, nil
		}
	}
	return nil, fmt.Errorf("getting port forwarding rule: %w", client.ErrNotFound)
}

func (f *fakeAPI) DeletePortForwardingRule(_ context.Context, id string) (*client.OperationReference, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.rules {
		if f.rules[i].ID == id {
			f.rules = append(f.rules[:i], f.rules[i+1:]...)
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
