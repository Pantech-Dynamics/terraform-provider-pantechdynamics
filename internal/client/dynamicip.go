package client

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Problem codes the Dynamic IP endpoints return.
const (
	CodeAddressRotationNotEnabled          = "ADDRESS_ROTATION_NOT_ENABLED"
	CodeAddressRotationOnDemandOff         = "ADDRESS_ROTATION_ON_DEMAND_OFF"
	CodeAddressRotationNeedsPrivateNetwork = "ADDRESS_ROTATION_NEEDS_PRIVATE_NETWORK"
	CodeAddressRotationNeedsAgent          = "ADDRESS_ROTATION_NEEDS_AGENT"
	CodeAddressRotationNeedsSecurityGroup  = "ADDRESS_ROTATION_NEEDS_SECURITY_GROUP"
	CodeAddressRotationNoAddress           = "ADDRESS_ROTATION_NO_ADDRESS"
	CodeAddressRotationUnavailable         = "ADDRESS_ROTATION_UNAVAILABLE"
	CodeAddressRotationBusy                = "ADDRESS_ROTATION_BUSY"
	CodeAddressRotationNotFound            = "ADDRESS_ROTATION_NOT_FOUND"
	CodeAddressRotationLimitReached        = "ADDRESS_ROTATION_LIMIT_REACHED"
)

// Dynamic IP rotation schedules.
const (
	DynamicIPScheduleEvery12Hours = "every_12_hours"
	DynamicIPScheduleDaily        = "daily"
	DynamicIPScheduleWeekly       = "weekly"
	DynamicIPScheduleMonthly      = "monthly"
)

// Dynamic IP connection handover modes.
const (
	DynamicIPConnectionsKeep = "keep"
	DynamicIPConnectionsMove = "move"
)

// Dynamic IP lifecycle states.
const (
	DynamicIPStateEnabling  = "enabling"
	DynamicIPStateActive    = "active"
	DynamicIPStateDegraded  = "degraded"
	DynamicIPStateDisabling = "disabling"
	DynamicIPStateOff       = "off"
	DynamicIPStateFailed    = "failed"
)

// DynamicIP is the rotating outbound public address configuration and current
// state of an instance.
type DynamicIP struct {
	InstanceID       string           `json:"instance_id"`
	Schedule         string           `json:"schedule"`
	Connections      string           `json:"connections"`
	OnDemand         bool             `json:"on_demand"`
	State            string           `json:"state"`
	CurrentAddress   *string          `json:"current_address"`
	NextRotationAt   *time.Time       `json:"next_rotation_at"`
	LastRotatedAt    *time.Time       `json:"last_rotated_at"`
	SurchargePercent float64          `json:"surcharge_percent"`
	OnDemandUsed     int64            `json:"on_demand_used"`
	OnDemandLimit    int64            `json:"on_demand_limit"`
	FailureReason    *string          `json:"failure_reason"`
	Events           []DynamicIPEvent `json:"events"`
}

// DynamicIPEvent is one recorded address change for Dynamic IP.
type DynamicIPEvent struct {
	Kind        string     `json:"kind"`
	FromAddress *string    `json:"from_address"`
	ToAddress   *string    `json:"to_address"`
	Degraded    bool       `json:"degraded"`
	OccurredAt  *time.Time `json:"occurred_at"`
}

// PutDynamicIPRequest enables or updates Dynamic IP on an instance.
type PutDynamicIPRequest struct {
	Schedule    string `json:"schedule"`
	Connections string `json:"connections,omitempty"`
	OnDemand    *bool  `json:"on_demand,omitempty"`
}

// CreateInstanceDynamicIP turns on Dynamic IP when an instance is created.
type CreateInstanceDynamicIP struct {
	Schedule    string `json:"schedule"`
	Connections string `json:"connections,omitempty"`
}

func dynamicIPPath(instanceID string) string {
	return instancePath(instanceID) + "/address-rotation"
}

// GetDynamicIP returns the Dynamic IP configuration and status for an instance.
// It returns ErrNotFound when Dynamic IP is not enabled or the instance does
// not exist.
func (c *Client) GetDynamicIP(ctx context.Context, instanceID string) (*DynamicIP, error) {
	var dip DynamicIP
	if err := c.do(ctx, http.MethodGet, dynamicIPPath(instanceID), nil, &dip); err != nil {
		return nil, fmt.Errorf("getting Dynamic IP for instance %s: %w", instanceID, err)
	}
	return &dip, nil
}

// PutDynamicIP enables Dynamic IP (201) or updates its settings (200) on an
// instance. It replays on the same Idempotency-Key.
func (c *Client) PutDynamicIP(ctx context.Context, instanceID string, req PutDynamicIPRequest) (*DynamicIP, error) {
	var dip DynamicIP
	if err := c.do(ctx, http.MethodPut, dynamicIPPath(instanceID), req, &dip, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating Dynamic IP for instance %s: %w", instanceID, err)
	}
	return &dip, nil
}

// DeleteDynamicIP disables Dynamic IP on an instance. It returns the resulting
// state (disabling or off), or ErrNotFound if Dynamic IP is already off or the
// instance does not exist. It replays on the same Idempotency-Key.
func (c *Client) DeleteDynamicIP(ctx context.Context, instanceID string) (*DynamicIP, error) {
	var dip DynamicIP
	if err := c.do(ctx, http.MethodDelete, dynamicIPPath(instanceID), nil, &dip, replaySafe()); err != nil {
		return nil, fmt.Errorf("disabling Dynamic IP for instance %s: %w", instanceID, err)
	}
	return &dip, nil
}

// RotateDynamicIP requests an immediate address rotation on an instance. It is
// not retried automatically so a lost response cannot trigger a second rotation
// or spend another on-demand quota slot.
func (c *Client) RotateDynamicIP(ctx context.Context, instanceID string) (*DynamicIP, error) {
	var dip DynamicIP
	if err := c.do(ctx, http.MethodPost, dynamicIPPath(instanceID)+"/rotate", nil, &dip); err != nil {
		return nil, fmt.Errorf("rotating Dynamic IP for instance %s: %w", instanceID, err)
	}
	return &dip, nil
}
