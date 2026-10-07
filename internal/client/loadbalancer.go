package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the load balancer endpoints return.
const (
	// CodePublicIPNotForLoadBalancer refuses a create on an address that is not
	// an active public IP with purpose load_balancer.
	CodePublicIPNotForLoadBalancer = "PUBLIC_IP_NOT_FOR_LOAD_BALANCER"

	// CodePortInUse refuses a create whose public port another load balancer on
	// the same address already uses.
	CodePortInUse = "PORT_IN_USE"

	// CodeLoadBalancerNotChangeable refuses a change to a load balancer that is
	// being deleted or has failed.
	CodeLoadBalancerNotChangeable = "LOAD_BALANCER_NOT_CHANGEABLE"

	// CodePublicIPHasLoadBalancers refuses releasing a public IP that still
	// carries load balancers.
	CodePublicIPHasLoadBalancers = "PUBLIC_IP_HAS_LOAD_BALANCERS"
)

// LoadBalancerMember is one target instance of a load balancer.
type LoadBalancerMember struct {
	InstanceID    string  `json:"instance_id"`
	InstanceName  *string `json:"instance_name"`
	DesiredState  string  `json:"desired_state"`
	ObservedState string  `json:"observed_state"`
}

// LoadBalancer spreads one TCP port of a load_balancer public IP across
// instances in one subnet of a VPC network.
type LoadBalancer struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	PublicIPID      string               `json:"public_ip_id"`
	PublicIPAddress *string              `json:"public_ip_address"`
	NetworkID       *string              `json:"network_id"`
	SubnetID        string               `json:"subnet_id"`
	Protocol        string               `json:"protocol"`
	Algorithm       string               `json:"algorithm"`
	PublicPort      int64                `json:"public_port"`
	PrivatePort     int64                `json:"private_port"`
	CIDRList        []string             `json:"cidr_list"`
	Members         []LoadBalancerMember `json:"members"`
	DesiredState    string               `json:"desired_state"`
	ObservedState   string               `json:"observed_state"`
	InSync          bool                 `json:"in_sync"`
	CreatedAt       *time.Time           `json:"created_at"`
	UpdatedAt       *time.Time           `json:"updated_at"`
}

// CreateLoadBalancerRequest creates a load balancer. Unset optional fields are
// omitted, so the backend applies its defaults (roundrobin, private_port equal
// to public_port, any source, no targets).
type CreateLoadBalancerRequest struct {
	Name        string   `json:"name"`
	PublicIPID  string   `json:"public_ip_id"`
	SubnetID    string   `json:"subnet_id"`
	Algorithm   string   `json:"algorithm,omitempty"`
	PublicPort  int64    `json:"public_port"`
	PrivatePort *int64   `json:"private_port,omitempty"`
	CIDRList    []string `json:"cidr_list,omitempty"`
	InstanceIDs []string `json:"instance_ids,omitempty"`
}

// UpdateLoadBalancerRequest changes a load balancer. A nil field is left as it
// is. A non-nil InstanceIDs is the whole new target set, so an empty, non-nil
// slice removes every target and is sent as [].
type UpdateLoadBalancerRequest struct {
	Name        *string   `json:"name,omitempty"`
	Algorithm   *string   `json:"algorithm,omitempty"`
	InstanceIDs *[]string `json:"instance_ids,omitempty"`
}

// CreateLoadBalancer starts creating a load balancer. Its id is the returned
// ResourceID.
func (c *Client) CreateLoadBalancer(ctx context.Context, req CreateLoadBalancerRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, "/load-balancers", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating load balancer: %w", err)
	}
	return &ref, nil
}

// ListLoadBalancers returns every load balancer of the account, following the
// cursor. Deleted and failed ones are left out by the backend.
func (c *Client) ListLoadBalancers(ctx context.Context) ([]LoadBalancer, error) {
	lbs, err := listAll[LoadBalancer](ctx, c, "/load-balancers")
	if err != nil {
		return nil, fmt.Errorf("listing load balancers: %w", err)
	}
	return lbs, nil
}

// GetLoadBalancer returns one load balancer, or ErrNotFound.
func (c *Client) GetLoadBalancer(ctx context.Context, id string) (*LoadBalancer, error) {
	var lb LoadBalancer
	if err := c.do(ctx, http.MethodGet, loadBalancerPath(id), nil, &lb); err != nil {
		return nil, fmt.Errorf("getting load balancer %s: %w", id, err)
	}
	return &lb, nil
}

// UpdateLoadBalancer starts changing a load balancer. An empty OperationID in
// the result means the request changed nothing and there is nothing to wait for.
func (c *Client) UpdateLoadBalancer(ctx context.Context, id string, req UpdateLoadBalancerRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPatch, loadBalancerPath(id), req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating load balancer %s: %w", id, err)
	}
	return &ref, nil
}

// DeleteLoadBalancer starts deleting a load balancer. The instances and the
// public IP are kept.
func (c *Client) DeleteLoadBalancer(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, loadBalancerPath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting load balancer %s: %w", id, err)
	}
	return &ref, nil
}

func loadBalancerPath(id string) string {
	return "/load-balancers/" + url.PathEscape(id)
}
