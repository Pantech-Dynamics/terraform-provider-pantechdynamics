package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the security group endpoints return on a 409.
const (
	// CodeDefaultSecurityGroupUndeletable means the group is the organization's
	// default one, which can never be deleted.
	CodeDefaultSecurityGroupUndeletable = "DEFAULT_SECURITY_GROUP_UNDELETABLE"

	// CodeInvalidResourceState is returned, for a delete, while an instance still
	// uses the group.
	CodeInvalidResourceState = "INVALID_RESOURCE_STATE"

	// CodeSecurityGroupAttachedToDatabase means a database still uses the group
	// as a source of access ranges. Detach it from the database first.
	CodeSecurityGroupAttachedToDatabase = "SECURITY_GROUP_ATTACHED_TO_DATABASE"
)

// SecurityGroupRule is one firewall rule. PortRange is a single port ("22"), an
// inclusive range ("8000-8080"), or an empty string for every port and for icmp.
// There is no wildcard, and the field must be sent even when empty.
type SecurityGroupRule struct {
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	PortRange string `json:"port_range"`
	CIDR      string `json:"cidr"`
}

// SecurityGroup is a reusable firewall for standard instances.
type SecurityGroup struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Rules         []SecurityGroupRule `json:"rules"`
	DesiredState  string              `json:"desired_state"`
	ObservedState string              `json:"observed_state"`
	CreatedAt     *time.Time          `json:"created_at"`
	UpdatedAt     *time.Time          `json:"updated_at"`
}

// CreateSecurityGroupRequest creates a group. The backend requires at least one rule.
type CreateSecurityGroupRequest struct {
	Name  string              `json:"name"`
	Rules []SecurityGroupRule `json:"rules"`
}

// replaceRulesRequest replaces a group's entire rule set.
type replaceRulesRequest struct {
	Rules []SecurityGroupRule `json:"rules"`
}

// CreateSecurityGroup starts creating a group. The call returns once the backend
// has accepted it; wait for the returned operation to know the outcome. It is
// retried on gateway errors because the backend replays it on an Idempotency-Key.
func (c *Client) CreateSecurityGroup(ctx context.Context, req CreateSecurityGroupRequest) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, "/security-groups", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating security group: %w", err)
	}
	return &ref, nil
}

// GetSecurityGroup returns one group. It returns ErrNotFound when it does not exist.
func (c *Client) GetSecurityGroup(ctx context.Context, id string) (*SecurityGroup, error) {
	var sg SecurityGroup
	if err := c.do(ctx, http.MethodGet, securityGroupPath(id), nil, &sg); err != nil {
		return nil, fmt.Errorf("getting security group %s: %w", id, err)
	}
	return &sg, nil
}

// ListSecurityGroups returns every group, following the cursor.
func (c *Client) ListSecurityGroups(ctx context.Context) ([]SecurityGroup, error) {
	groups, err := listAll[SecurityGroup](ctx, c, "/security-groups")
	if err != nil {
		return nil, fmt.Errorf("listing security groups: %w", err)
	}
	return groups, nil
}

// ReplaceSecurityGroupRules starts replacing the group's whole rule set. A rule
// that is not in rules is removed, and every instance using the group is affected.
func (c *Client) ReplaceSecurityGroupRules(ctx context.Context, id string, rules []SecurityGroupRule) (*OperationReference, error) {
	var ref OperationReference
	path := securityGroupPath(id) + "/rules"
	if err := c.do(ctx, http.MethodPut, path, replaceRulesRequest{Rules: rules}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("replacing rules of security group %s: %w", id, err)
	}
	return &ref, nil
}

// DeleteSecurityGroup starts deleting a group. It returns ErrNotFound when the
// group is already gone, and an APIError with CodeInvalidResourceState while an
// instance still uses it.
func (c *Client) DeleteSecurityGroup(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, securityGroupPath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting security group %s: %w", id, err)
	}
	return &ref, nil
}

// securityGroupPath escapes the id so a malformed value cannot alter the URL path.
func securityGroupPath(id string) string {
	return "/security-groups/" + url.PathEscape(id)
}
