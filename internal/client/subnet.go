package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Subnet is a slice of a network's address range. Instances attach to a subnet.
type Subnet struct {
	ID            string     `json:"id"`
	NetworkID     string     `json:"network_id"`
	Name          string     `json:"name"`
	CIDR          string     `json:"cidr"`
	Region        string     `json:"region"`
	Zone          string     `json:"zone"`
	InstanceCount *int64     `json:"instance_count"`
	DesiredState  string     `json:"desired_state"`
	ObservedState string     `json:"observed_state"`
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// CreateSubnetRequest creates a subnet. The CIDR must sit inside the network's.
// The backend does not check that up front: a CIDR outside the network fails the
// operation, not the request.
type CreateSubnetRequest struct {
	Name string `json:"name"`
	CIDR string `json:"cidr"`
}

// CreateSubnet starts creating a subnet in a network.
func (c *Client) CreateSubnet(ctx context.Context, networkID string, req CreateSubnetRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, networkPath(networkID)+"/subnets", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating subnet in network %s: %w", networkID, err)
	}
	return &ref, nil
}

// GetSubnet returns one subnet, or ErrNotFound.
func (c *Client) GetSubnet(ctx context.Context, id string) (*Subnet, error) {
	var s Subnet
	if err := c.do(ctx, http.MethodGet, subnetPath(id), nil, &s); err != nil {
		return nil, fmt.Errorf("getting subnet %s: %w", id, err)
	}
	return &s, nil
}

// DeleteSubnet starts deleting a subnet. The backend refuses while instances
// remain in it.
func (c *Client) DeleteSubnet(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, subnetPath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting subnet %s: %w", id, err)
	}
	return &ref, nil
}

func subnetPath(id string) string {
	return "/subnets/" + url.PathEscape(id)
}
