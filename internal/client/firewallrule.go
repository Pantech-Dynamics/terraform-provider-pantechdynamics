package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// FirewallRule is one stateless rule on a VPC subnet. Rules cannot be edited:
// a change is a delete and an add. Number orders evaluation, lowest first.
type FirewallRule struct {
	ID            string     `json:"id"`
	SubnetID      string     `json:"subnet_id"`
	Number        int64      `json:"number"`
	Direction     string     `json:"direction"`
	Protocol      string     `json:"protocol"`
	PortStart     *int64     `json:"port_start"`
	PortEnd       *int64     `json:"port_end"`
	ICMPType      *int64     `json:"icmp_type"`
	ICMPCode      *int64     `json:"icmp_code"`
	CIDR          string     `json:"cidr"`
	Action        string     `json:"action"`
	DesiredState  string     `json:"desired_state"`
	ObservedState string     `json:"observed_state"`
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// CreateFirewallRuleRequest adds a rule. Pointers are omitted when nil, because
// the backend wants port fields only for tcp and udp and icmp fields only for icmp.
type CreateFirewallRuleRequest struct {
	Number    int64  `json:"number"`
	Direction string `json:"direction,omitempty"`
	Protocol  string `json:"protocol"`
	PortStart *int64 `json:"port_start,omitempty"`
	PortEnd   *int64 `json:"port_end,omitempty"`
	ICMPType  *int64 `json:"icmp_type,omitempty"`
	ICMPCode  *int64 `json:"icmp_code,omitempty"`
	CIDR      string `json:"cidr"`
	Action    string `json:"action,omitempty"`
}

// listFirewallRulesResponse accepts both list shapes: the spec's "rules" and the
// "data" every verified list uses. The live shape is unverified.
type listFirewallRulesResponse struct {
	Rules []FirewallRule `json:"rules"`
	Data  []FirewallRule `json:"data"`
}

// CreateFirewallRule starts adding a rule to a subnet.
func (c *Client) CreateFirewallRule(ctx context.Context, subnetID string, req CreateFirewallRuleRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, subnetPath(subnetID)+"/firewall-rules", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating firewall rule on subnet %s: %w", subnetID, err)
	}
	return &ref, nil
}

// ListFirewallRules returns the customer's own rules on a subnet. It returns
// ErrNotFound when the subnet is gone.
func (c *Client) ListFirewallRules(ctx context.Context, subnetID string) ([]FirewallRule, error) {
	var page listFirewallRulesResponse
	if err := c.do(ctx, http.MethodGet, subnetPath(subnetID)+"/firewall-rules", nil, &page); err != nil {
		return nil, fmt.Errorf("listing firewall rules of subnet %s: %w", subnetID, err)
	}
	return append(page.Rules, page.Data...), nil
}

// GetFirewallRule finds one rule by listing its subnet, because the spec has no
// single-rule read. It returns ErrNotFound when the rule or the subnet is gone.
func (c *Client) GetFirewallRule(ctx context.Context, subnetID, id string) (*FirewallRule, error) {
	rules, err := c.ListFirewallRules(ctx, subnetID)
	if err != nil {
		return nil, err
	}
	for i := range rules {
		if rules[i].ID == id {
			return &rules[i], nil
		}
	}
	return nil, fmt.Errorf("getting firewall rule %s: %w", id, ErrNotFound)
}

// DeleteFirewallRule starts removing a rule.
func (c *Client) DeleteFirewallRule(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, "/firewall-rules/"+url.PathEscape(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting firewall rule %s: %w", id, err)
	}
	return &ref, nil
}
