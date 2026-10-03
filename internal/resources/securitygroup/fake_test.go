package securitygroup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// fakeAPI is an in-memory groupAPI. A non-nil *Err is returned by the matching
// method. opStuck makes the operation never finish on its own, as a delete
// operation did on dev, so a wait only succeeds when its done check passes.
type fakeAPI struct {
	groups []client.SecurityGroup

	createErr, getErr, listErr, replaceErr, deleteErr, waitErr error
	opStuck                                                    bool

	nextID      int
	creates     int
	replaces    int
	deletes     int
	waits       int
	lastCreate  client.CreateSecurityGroupRequest
	lastReplace []client.SecurityGroupRule
}

var errConnReset = errors.New("connection reset by peer")

func now() *time.Time {
	t := time.Date(2026, 10, 3, 22, 57, 3, 517994000, time.UTC)
	return &t
}

func (f *fakeAPI) CreateSecurityGroup(_ context.Context, req client.CreateSecurityGroupRequest) (*client.OperationReference, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("sg_%d", f.nextID)
	f.groups = append(f.groups, client.SecurityGroup{
		ID: id, Name: req.Name, Rules: req.Rules, DesiredState: "present", ObservedState: "active", CreatedAt: now(), UpdatedAt: now(),
	})
	return &client.OperationReference{OperationID: "op_" + id, ResourceID: id, Status: client.OperationSubmitting}, nil
}

func (f *fakeAPI) GetSecurityGroup(_ context.Context, id string) (*client.SecurityGroup, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.groups {
		if f.groups[i].ID == id {
			g := f.groups[i]
			return &g, nil
		}
	}
	return nil, fmt.Errorf("getting security group: %w", client.ErrNotFound)
}

func (f *fakeAPI) ListSecurityGroups(context.Context) ([]client.SecurityGroup, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]client.SecurityGroup(nil), f.groups...), nil
}

func (f *fakeAPI) ReplaceSecurityGroupRules(_ context.Context, id string, rules []client.SecurityGroupRule) (*client.OperationReference, error) {
	f.replaces++
	f.lastReplace = rules
	if f.replaceErr != nil {
		return nil, f.replaceErr
	}
	for i := range f.groups {
		if f.groups[i].ID == id {
			f.groups[i].Rules = rules
		}
	}
	return &client.OperationReference{OperationID: "op_put", ResourceID: id}, nil
}

func (f *fakeAPI) DeleteSecurityGroup(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for i := range f.groups {
		if f.groups[i].ID == id {
			f.groups = append(f.groups[:i], f.groups[i+1:]...)
			break
		}
	}
	return &client.OperationReference{OperationID: "op_del", ResourceID: id}, nil
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	f.waits++
	if f.waitErr != nil {
		return f.waitErr
	}
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
		return errors.New("operation never finished and the done check did not pass")
	}
	return nil
}
