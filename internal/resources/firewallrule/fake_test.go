package firewallrule

import (
	"context"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory ruleAPI. A non-nil *Err is returned by the matching method.
type fakeAPI struct {
	rules []client.FirewallRule

	createErr, getErr, deleteErr, waitErr error
	createState                           string
	opStuck, keepOnDelete                 bool
	op                                    *client.Operation

	nextID       int
	creates      int
	deletes      int
	lastSubnetID string
	lastCreate   client.CreateFirewallRuleRequest
}

func now() *time.Time {
	t := time.Date(2026, 10, 4, 15, 0, 0, 123000000, time.UTC)
	return &t
}

func (f *fakeAPI) CreateFirewallRule(_ context.Context, subnetID string, req client.CreateFirewallRuleRequest) (*client.OperationReference, error) {
	f.creates++
	f.lastSubnetID, f.lastCreate = subnetID, req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("aclr_%d", f.nextID)
	state := f.createState
	if state == "" {
		state = "active"
	}
	rule := client.FirewallRule{
		ID: id, SubnetID: subnetID, Number: req.Number, Direction: orDefault(req.Direction, "ingress"), Protocol: req.Protocol,
		PortStart: req.PortStart, PortEnd: req.PortEnd, ICMPType: req.ICMPType, ICMPCode: req.ICMPCode,
		CIDR: req.CIDR, Action: orDefault(req.Action, "allow"), DesiredState: "present", ObservedState: state, CreatedAt: now(), UpdatedAt: now(),
	}
	if rule.PortEnd == nil {
		rule.PortEnd = rule.PortStart // the backend defaults the end to the start
	}
	f.rules = append(f.rules, rule)
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id}, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (f *fakeAPI) GetFirewallRule(_ context.Context, subnetID, id string) (*client.FirewallRule, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.rules {
		if f.rules[i].ID == id && f.rules[i].SubnetID == subnetID {
			r := f.rules[i]
			return &r, nil
		}
	}
	return nil, fmt.Errorf("getting firewall rule: %w", client.ErrNotFound)
}

func (f *fakeAPI) DeleteFirewallRule(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.rules {
		if f.rules[i].ID == id && !f.keepOnDelete {
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
