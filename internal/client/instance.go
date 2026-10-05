package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the instance endpoints return.
const (
	// CodeInsufficientCredit means the account has too little credit and no
	// default card. Its detail states the amount required and the amount available.
	CodeInsufficientCredit = "INSUFFICIENT_CREDIT"

	// CodeInstanceHasPublicIP and CodeInstanceHasPortForwards refuse a delete
	// while a public IP or port forwarding rule still points at a VPC instance.
	CodeInstanceHasPublicIP     = "INSTANCE_HAS_PUBLIC_IP"
	CodeInstanceHasPortForwards = "INSTANCE_HAS_PORT_FORWARDS"

	// CodeInstanceMustBeStopped refuses a security group change on a running instance.
	CodeInstanceMustBeStopped = "INSTANCE_MUST_BE_STOPPED"

	// CodeSecurityGroupAllowsPrivateNetwork refuses a private network attach
	// (409), or a group change while attached (422 on security_group_id), when
	// the security group lets in any part of the private database network's
	// range. The detail names the rules to narrow.
	CodeSecurityGroupAllowsPrivateNetwork = "SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK"

	// CodePrivateNetworkNotAvailable means the instance's zone has no private
	// database network, or the instance is not a standard one.
	CodePrivateNetworkNotAvailable = "PRIVATE_NETWORK_NOT_AVAILABLE"

	// FieldCodePlanNotBigger is the field error on plan_slug for a resize to the
	// same or a smaller plan. Instances only grow.
	FieldCodePlanNotBigger = "PLAN_NOT_BIGGER"
)

// Instance states reported in ObservedState.
const (
	InstanceRunning = "running"
	InstanceStopped = "stopped"
	InstanceFailed  = "failed"
	InstanceDeleted = "deleted"

	// Transitional states: the instance is between two settled states.
	InstancePending      = "pending"
	InstanceProvisioning = "provisioning"
	InstanceStopping     = "stopping"
	InstanceDeleting     = "deleting"
)

// Private network interface states reported in PrivateNetworkState.
const (
	PrivateNetworkNone      = "none"
	PrivateNetworkAttaching = "attaching"
	PrivateNetworkAttached  = "attached"
	PrivateNetworkDetaching = "detaching"
)

// InstanceSpec is the compute capacity pinned to an instance.
type InstanceSpec struct {
	VCPU     int64 `json:"vcpu"`
	MemoryMB int64 `json:"memory_mb"`
	DiskGB   int64 `json:"disk_gb"`
}

// InstanceFailure says why an instance failed. Its shape is unverified: no
// instance has failed on dev yet, and a healthy one reports null.
type InstanceFailure struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Instance is a virtual machine. The fields follow the live API, which differs
// from the OpenAPI spec (it has zone, public_ipv4, private_ipv4 and spec).
type Instance struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	PlanID          string  `json:"plan_id"`
	PlanSlug        string  `json:"plan_slug"`
	ImageID         string  `json:"image_id"`
	ImageSlug       string  `json:"image_slug"`
	Region          string  `json:"region"`
	Zone            string  `json:"zone"`
	NetworkID       *string `json:"network_id"`
	SubnetID        *string `json:"subnet_id"`
	SecurityGroupID string  `json:"security_group_id"`
	PublicIPv4      *string `json:"public_ipv4"`
	PrivateIPv4     *string `json:"private_ipv4"`
	// PrivateNetworkState is the interface on the zone's private database
	// network: none, attaching, attached or detaching. PrivateNetworkIP is its
	// address once attached, kept while it is being removed.
	PrivateNetworkState string            `json:"private_network_state"`
	PrivateNetworkIP    *string           `json:"private_network_ip"`
	DesiredState        string            `json:"desired_state"`
	ObservedState       string            `json:"observed_state"`
	Spec                InstanceSpec      `json:"spec"`
	Tags                map[string]string `json:"tags"`
	Failure             *InstanceFailure  `json:"failure"`
	CreatedAt           *time.Time        `json:"created_at"`
	UpdatedAt           *time.Time        `json:"updated_at"`
}

// CreateInstanceRequest orders an instance. Empty optional fields are omitted so
// the backend applies its defaults (the default region and security group).
type CreateInstanceRequest struct {
	Name            string            `json:"name"`
	PlanSlug        string            `json:"plan_slug"`
	ImageSlug       string            `json:"image_slug"`
	SSHKeyID        string            `json:"ssh_key_id,omitempty"`
	Region          string            `json:"region,omitempty"`
	SecurityGroupID string            `json:"security_group_id,omitempty"`
	SubnetID        string            `json:"subnet_id,omitempty"`
	NetworkID       string            `json:"network_id,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// renameRequest changes an instance's name.
type renameRequest struct {
	Name string `json:"name"`
}

// resizeRequest asks for a bigger plan.
type resizeRequest struct {
	PlanSlug string `json:"plan_slug"`
}

// securityGroupRequest names the group an instance should use.
type securityGroupRequest struct {
	SecurityGroupID string `json:"security_group_id"`
}

// CreateInstance orders an instance and returns the order to follow. This spends
// money, so it is worth knowing why retrying it is safe: the backend replays a
// create on the same Idempotency-Key and returns the same order, verified on dev,
// so a retry after a gateway error or a lost response cannot charge twice.
func (c *Client) CreateInstance(ctx context.Context, req CreateInstanceRequest) (*InstanceOrderReference, error) {
	var ref InstanceOrderReference
	if err := c.do(ctx, http.MethodPost, "/instances", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating instance: %w", err)
	}
	return &ref, nil
}

// GetInstance returns one instance. It returns ErrNotFound when it does not exist.
func (c *Client) GetInstance(ctx context.Context, id string) (*Instance, error) {
	var inst Instance
	if err := c.do(ctx, http.MethodGet, instancePath(id), nil, &inst); err != nil {
		return nil, fmt.Errorf("getting instance %s: %w", id, err)
	}
	return &inst, nil
}

// ListInstances returns every instance, following the cursor.
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	instances, err := listAll[Instance](ctx, c, "/instances")
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	return instances, nil
}

// RenameInstance starts renaming an instance. Wait for the returned operation.
// It is retried on gateway errors because the public API replays every write on the same Idempotency-Key except ssh-key create and delete and the console (spec, 2026-10-05).
func (c *Client) RenameInstance(ctx context.Context, id, name string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, instancePath(id)+"/rename", renameRequest{Name: name}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("renaming instance %s: %w", id, err)
	}
	return &ref, nil
}

// DeleteInstance starts deleting an instance. A deleted instance stays readable
// with observed_state "deleted", so wait for that or for ErrNotFound. It is
// retried on gateway errors because the backend replays a delete on the same
// Idempotency-Key, verified on dev.
func (c *Client) DeleteInstance(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, instancePath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting instance %s: %w", id, err)
	}
	return &ref, nil
}

// StartInstance starts a stopped instance and returns the operation to follow.
// Only call it when the instance is stopped: starting a running instance makes
// the operation fail and leaves the instance in the failed state. It is retried
// on gateway errors because the backend replays a start on the same
// Idempotency-Key, verified on staging.
func (c *Client) StartInstance(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, instancePath(id)+"/start", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("starting instance %s: %w", id, err)
	}
	return &ref, nil
}

// StopInstance stops a running instance and returns the operation to follow.
// Only call it when the instance is running: stopping a stopped instance makes
// the operation fail and leaves the instance in the failed state. It is retried
// on gateway errors because a replay with the same Idempotency-Key returns the
// original operation instead of sending a second stop: the public API replays every write on the same Idempotency-Key except ssh-key create and delete and the console.
func (c *Client) StopInstance(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, instancePath(id)+"/stop", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("stopping instance %s: %w", id, err)
	}
	return &ref, nil
}

// ResizeInstance moves a running instance to a bigger plan and returns the
// operation to follow. The platform stops, resizes and restarts it, which took
// about 4.5 minutes on staging, and the disk grows with the plan. A plan that is
// not bigger is refused with the field error PLAN_NOT_BIGGER, and a stopped
// instance with 409. It is retried on gateway errors because the public API replays every write on the same Idempotency-Key except ssh-key create and delete and the console.
func (c *Client) ResizeInstance(ctx context.Context, id, planSlug string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, instancePath(id)+"/resize", resizeRequest{PlanSlug: planSlug}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("resizing instance %s: %w", id, err)
	}
	return &ref, nil
}

// ChangeInstanceSecurityGroup points a stopped instance at another security group
// and returns the operation to follow. A running instance is refused with
// CodeInstanceMustBeStopped, and the new group applies when it starts again. It
// is retried on gateway errors because the backend replays it on the same
// Idempotency-Key, verified on staging.
func (c *Client) ChangeInstanceSecurityGroup(ctx context.Context, id, securityGroupID string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPut, instancePath(id)+"/security-group", securityGroupRequest{SecurityGroupID: securityGroupID}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("changing the security group of instance %s: %w", id, err)
	}
	return &ref, nil
}

// AttachInstancePrivateNetwork adds an interface on the zone's private database
// network to a running or stopped standard instance. A security group that lets
// in the private range is refused with CodeSecurityGroupAllowsPrivateNetwork. It
// replays on the same key.
func (c *Client) AttachInstancePrivateNetwork(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, instancePath(id)+"/private-network", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("attaching instance %s to the private network: %w", id, err)
	}
	return &ref, nil
}

// DetachInstancePrivateNetwork removes the private database network interface.
// It replays on the same key.
func (c *Client) DetachInstancePrivateNetwork(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, instancePath(id)+"/private-network", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("detaching instance %s from the private network: %w", id, err)
	}
	return &ref, nil
}

// instancePath escapes the id so a malformed value cannot alter the URL path.
func instancePath(id string) string {
	return "/instances/" + url.PathEscape(id)
}
