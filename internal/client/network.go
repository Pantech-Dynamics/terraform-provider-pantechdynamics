package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Network is a private network (a VPC). Subnets live inside it. The live API
// names the location "zone", not the spec's "zone_id".
type Network struct {
	ID            string     `json:"id"`
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

// CreateNetworkRequest creates a network. Region is optional; omit it for the
// account's default region.
type CreateNetworkRequest struct {
	Name   string `json:"name"`
	CIDR   string `json:"cidr"`
	Region string `json:"region,omitempty"`
}

// CreateNetwork starts creating a network. It is retried on gateway errors
// because the backend replays it on an Idempotency-Key (verified on staging).
func (c *Client) CreateNetwork(ctx context.Context, req CreateNetworkRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, "/networks", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating network: %w", err)
	}
	return &ref, nil
}

// GetNetwork returns one network, or ErrNotFound.
func (c *Client) GetNetwork(ctx context.Context, id string) (*Network, error) {
	var n Network
	if err := c.do(ctx, http.MethodGet, networkPath(id), nil, &n); err != nil {
		return nil, fmt.Errorf("getting network %s: %w", id, err)
	}
	return &n, nil
}

// ListNetworks returns every live network, following the cursor. A failed or
// deleted network is not listed.
func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	networks, err := listAll[Network](ctx, c, "/networks")
	if err != nil {
		return nil, fmt.Errorf("listing networks: %w", err)
	}
	return networks, nil
}

// DeleteNetwork starts deleting a network. The backend refuses while instances
// or public IPs remain in it.
func (c *Client) DeleteNetwork(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, networkPath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting network %s: %w", id, err)
	}
	return &ref, nil
}

func networkPath(id string) string {
	return "/networks/" + url.PathEscape(id)
}
