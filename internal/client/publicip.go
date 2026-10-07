package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Public IP purposes.
const (
	PublicIPStaticNAT      = "static_nat"
	PublicIPPortForwarding = "port_forwarding"
	PublicIPLoadBalancer   = "load_balancer"
)

// Problem codes a public IP create returns (403).
const (
	// CodePublicIPLimitExceeded means the organization holds as many live
	// public IPs, of any purpose, as it may (20 by default).
	CodePublicIPLimitExceeded = "PUBLIC_IP_LIMIT_EXCEEDED"

	// CodeStaticNATLimitExceeded means the organization holds as many
	// static_nat public IPs as it may.
	CodeStaticNATLimitExceeded = "STATIC_NAT_LIMIT_EXCEEDED"

	// CodeInstanceAlreadyHasPublicIP refuses a create or an attach naming an
	// instance that already has a static_nat address of its own (409).
	CodeInstanceAlreadyHasPublicIP = "INSTANCE_ALREADY_HAS_PUBLIC_IP"

	// CodePublicIPNotStaticNAT refuses an attach or detach of a port_forwarding
	// or load_balancer address (409): only static_nat addresses move.
	CodePublicIPNotStaticNAT = "PUBLIC_IP_NOT_STATIC_NAT"

	// FieldCodeInstanceNotInNetwork is the 422 field error on instance_id when
	// the instance is not in the address's VPC network.
	FieldCodeInstanceNotInNetwork = "INSTANCE_NOT_IN_NETWORK"
)

// PublicIP is a public IPv4 address on a VPC network. A static_nat address maps
// every port to one instance. A port_forwarding address carries forwarding rules.
// A load_balancer address carries load balancers, one per public port.
// The location field is "zone", as on networks and subnets, not "zone_id".
type PublicIP struct {
	ID            string     `json:"id"`
	NetworkID     string     `json:"network_id"`
	NetworkName   *string    `json:"network_name"`
	Zone          *string    `json:"zone"`
	Region        *string    `json:"region"`
	Purpose       string     `json:"purpose"`
	InstanceID    *string    `json:"instance_id"`
	InstanceName  *string    `json:"instance_name"`
	Address       *string    `json:"address"`
	DesiredState  string     `json:"desired_state"`
	ObservedState string     `json:"observed_state"`
	InSync        bool       `json:"in_sync"`
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// CreatePublicIPRequest allocates an address. InstanceID is optional for
// static_nat (left out, the address is reserved and attached later) and must be
// empty for port_forwarding and load_balancer.
type CreatePublicIPRequest struct {
	NetworkID  string `json:"network_id"`
	Purpose    string `json:"purpose,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

// CreatePublicIP starts allocating an address. It bills monthly.
func (c *Client) CreatePublicIP(ctx context.Context, req CreatePublicIPRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, "/public-ips", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating public ip: %w", err)
	}
	return &ref, nil
}

// GetPublicIP returns one address, or ErrNotFound.
func (c *Client) GetPublicIP(ctx context.Context, id string) (*PublicIP, error) {
	var ip PublicIP
	if err := c.do(ctx, http.MethodGet, publicIPPath(id), nil, &ip); err != nil {
		return nil, fmt.Errorf("getting public ip %s: %w", id, err)
	}
	return &ip, nil
}

// DeletePublicIP starts releasing an address. Its port forwarding rules and load
// balancers must be removed first (409 PUBLIC_IP_HAS_LOAD_BALANCERS otherwise).
func (c *Client) DeletePublicIP(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, publicIPPath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting public ip %s: %w", id, err)
	}
	return &ref, nil
}

// attachPublicIPRequest is the attach body.
type attachPublicIPRequest struct {
	InstanceID string `json:"instance_id"`
}

// AttachPublicIP points a static_nat address at an instance in its network,
// keeping the address. An address attached elsewhere moves. The reference's
// OperationID is empty when the address already points there.
func (c *Client) AttachPublicIP(ctx context.Context, id, instanceID string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, publicIPPath(id)+"/attach", attachPublicIPRequest{InstanceID: instanceID}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("attaching public ip %s to %s: %w", id, instanceID, err)
	}
	return &ref, nil
}

// DetachPublicIP unmaps a static_nat address from its instance. The address
// stays held, and billed, until it is attached again or released. The
// reference's OperationID is empty when it was already detached.
func (c *Client) DetachPublicIP(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, publicIPPath(id)+"/detach", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("detaching public ip %s: %w", id, err)
	}
	return &ref, nil
}

func publicIPPath(id string) string {
	return "/public-ips/" + url.PathEscape(id)
}
