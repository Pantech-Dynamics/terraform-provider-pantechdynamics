package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// PortForwardingRule forwards a public port range of a port_forwarding public IP
// to the same-width private range on one instance.
type PortForwardingRule struct {
	ID               string     `json:"id"`
	PublicIPID       string     `json:"public_ip_id"`
	InstanceID       string     `json:"instance_id"`
	Protocol         string     `json:"protocol"`
	PublicPortStart  int64      `json:"public_port_start"`
	PublicPortEnd    int64      `json:"public_port_end"`
	PrivatePortStart int64      `json:"private_port_start"`
	PrivatePortEnd   int64      `json:"private_port_end"`
	InstanceName     *string    `json:"instance_name"`
	DesiredState     string     `json:"desired_state"`
	ObservedState    string     `json:"observed_state"`
	CreatedAt        *time.Time `json:"created_at"`
	UpdatedAt        *time.Time `json:"updated_at"`
}

// CreatePortForwardingRuleRequest adds a rule. Unset ends default on the
// backend to the start of the same range.
type CreatePortForwardingRuleRequest struct {
	InstanceID       string `json:"instance_id"`
	Protocol         string `json:"protocol,omitempty"`
	PublicPortStart  int64  `json:"public_port_start"`
	PublicPortEnd    *int64 `json:"public_port_end,omitempty"`
	PrivatePortStart *int64 `json:"private_port_start,omitempty"`
	PrivatePortEnd   *int64 `json:"private_port_end,omitempty"`
}

// CreatePortForwardingRule starts adding a rule to a public IP.
func (c *Client) CreatePortForwardingRule(ctx context.Context, publicIPID string, req CreatePortForwardingRuleRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, publicIPPath(publicIPID)+"/port-forwarding-rules", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating port forwarding rule on public ip %s: %w", publicIPID, err)
	}
	return &ref, nil
}

// ListPortForwardingRules returns the rules of a public IP, following the
// cursor, or ErrNotFound when the address is gone.
func (c *Client) ListPortForwardingRules(ctx context.Context, publicIPID string) ([]PortForwardingRule, error) {
	rules, err := listAll[PortForwardingRule](ctx, c, publicIPPath(publicIPID)+"/port-forwarding-rules")
	if err != nil {
		return nil, fmt.Errorf("listing port forwarding rules of public ip %s: %w", publicIPID, err)
	}
	return rules, nil
}

// GetPortForwardingRule returns one rule of a public IP. It returns ErrNotFound
// when the rule or the address is gone.
func (c *Client) GetPortForwardingRule(ctx context.Context, publicIPID, id string) (*PortForwardingRule, error) {
	var rule PortForwardingRule
	if err := c.do(ctx, http.MethodGet, publicIPPath(publicIPID)+"/port-forwarding-rules/"+url.PathEscape(id), nil, &rule); err != nil {
		return nil, fmt.Errorf("getting port forwarding rule %s: %w", id, err)
	}
	return &rule, nil
}

// DeletePortForwardingRule starts removing a rule.
func (c *Client) DeletePortForwardingRule(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, "/port-forwarding-rules/"+url.PathEscape(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting port forwarding rule %s: %w", id, err)
	}
	return &ref, nil
}
