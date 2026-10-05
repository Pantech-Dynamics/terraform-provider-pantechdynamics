package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Instance order statuses. Provisioned is the success state. Failed and
// payment_failed are terminal failures.
const (
	OrderAwaitingPayment = "awaiting_payment"
	OrderPaid            = "paid"
	OrderProvisioning    = "provisioning"
	OrderProvisioned     = "provisioned"
	OrderPaymentFailed   = "payment_failed"
	OrderFailed          = "failed"
)

// InstanceOrderReference is the 202 body of an instance create. Note that it
// names the order order_id, while reading the order back calls it id.
type InstanceOrderReference struct {
	OrderID     string  `json:"order_id"`
	InstanceID  string  `json:"instance_id"`
	Status      string  `json:"status"`
	OperationID *string `json:"operation_id"`
	FailureCode *string `json:"failure_code"`
	AmountMinor int64   `json:"amount_minor"`
	Currency    string  `json:"currency"`
}

// InstanceOrder is the payment and provisioning state of an instance order.
type InstanceOrder struct {
	ID          string     `json:"id"`
	InstanceID  string     `json:"instance_id"`
	Status      string     `json:"status"`
	OperationID *string    `json:"operation_id"`
	FailureCode *string    `json:"failure_code"`
	AmountMinor int64      `json:"amount_minor"`
	Currency    string     `json:"currency"`
	CreatedAt   *time.Time `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

// Order failure codes with their own explanation. Others are shown as the
// backend sends them.
const (
	// OrderFailurePaymentExpired: the order was not paid within one hour.
	OrderFailurePaymentExpired = "payment_expired"
	// OrderFailureOrganizationDeleted: the order was cancelled because its
	// organization was deleted. It ends payment_failed when it was still
	// awaiting payment, or failed when it had been paid (the payment is
	// returned to credit).
	OrderFailureOrganizationDeleted = "organization_deleted"
)

// orderFailureExplanation says what a known order failure code means, or ""
// for any other code.
func orderFailureExplanation(code *string) string {
	if code == nil {
		return ""
	}
	switch *code {
	case OrderFailurePaymentExpired:
		return "the order was not paid within one hour and expired, nothing was provisioned"
	case OrderFailureOrganizationDeleted:
		return "the order was cancelled because its organization was deleted, nothing was provisioned and any payment taken is returned to credit"
	}
	return ""
}

// OrderError is returned when an order ends in a failed state.
type OrderError struct {
	Order InstanceOrder
}

// Error carries the status, the failure code and the order id, so a user can
// quote them to support. A payment failure says so, because the fix is different.
func (e *OrderError) Error() string {
	msg := fmt.Sprintf("instance order %s ended as %s", e.Order.ID, e.Order.Status)
	if e.Order.FailureCode != nil && *e.Order.FailureCode != "" {
		msg += " (" + *e.Order.FailureCode + ")"
	}
	switch explanation := orderFailureExplanation(e.Order.FailureCode); {
	case explanation != "":
		msg += ": " + explanation
	case e.Order.Status == OrderPaymentFailed:
		msg += ": the payment was declined, nothing was provisioned"
	}
	return msg
}

// GetInstanceOrder returns an order. It returns ErrNotFound if it does not exist.
func (c *Client) GetInstanceOrder(ctx context.Context, id string) (*InstanceOrder, error) {
	var order InstanceOrder
	if err := c.do(ctx, http.MethodGet, "/instance-orders/"+url.PathEscape(id), nil, &order); err != nil {
		return nil, fmt.Errorf("getting instance order %s: %w", id, err)
	}
	return &order, nil
}

// WaitForInstanceOrder polls until the order is provisioned, and returns it so
// the caller can follow its operation. A failed or declined order is returned as
// an *OrderError. Callers set the deadline through ctx.
func (c *Client) WaitForInstanceOrder(ctx context.Context, id string) (*InstanceOrder, error) {
	var settled *InstanceOrder
	err := c.pollUntil(ctx, "instance order "+id, func(ctx context.Context) (bool, string, error) {
		order, err := c.GetInstanceOrder(ctx, id)
		if err != nil {
			return false, "", err
		}
		switch order.Status {
		case OrderProvisioned:
			settled = order
			return true, order.Status, nil
		case OrderFailed, OrderPaymentFailed:
			return false, order.Status, &OrderError{Order: *order}
		}
		return false, order.Status, nil
	})
	if err != nil {
		return nil, err
	}
	return settled, nil
}
