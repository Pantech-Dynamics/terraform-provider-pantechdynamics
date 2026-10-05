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
)

// PublicIP is a public IPv4 address on a VPC network. A static_nat address maps
// every port to one instance. A port_forwarding address carries forwarding rules.
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
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// CreatePublicIPRequest allocates an address. InstanceID is required for
// static_nat and must be empty for port_forwarding.
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

// DeletePublicIP starts releasing an address. Its port forwarding rules must be
// removed first.
func (c *Client) DeletePublicIP(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, publicIPPath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting public ip %s: %w", id, err)
	}
	return &ref, nil
}

func publicIPPath(id string) string {
	return "/public-ips/" + url.PathEscape(id)
}
