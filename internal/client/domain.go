package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the domain endpoints return.
const (
	// CodeDomainNotFound is the 404 for a domain that is not in the
	// organization. It counts as ErrNotFound.
	CodeDomainNotFound = "DOMAIN_NOT_FOUND"

	// CodeDomainOrderNotFound is the 404 for an unknown domain order. It
	// counts as ErrNotFound.
	CodeDomainOrderNotFound = "DOMAIN_ORDER_NOT_FOUND"

	// CodeDomainsUnavailable is the 503 when domains are not sold on this
	// deployment.
	CodeDomainsUnavailable = "DOMAINS_UNAVAILABLE"

	// CodeDomainNotAvailable is the 409 when the name is taken, premium or
	// reserved.
	CodeDomainNotAvailable = "DOMAIN_NOT_AVAILABLE"

	// CodeDomainAlreadyHeld is the 409 when the domain is already on Pantech.
	CodeDomainAlreadyHeld = "DOMAIN_ALREADY_HELD"

	// CodeTLDNotSold is the 422 for a TLD Pantech does not sell.
	CodeTLDNotSold = "TLD_NOT_SOLD"

	// CodeDomainOrderInFlight is the 409 while another order for the domain
	// runs.
	CodeDomainOrderInFlight = "DOMAIN_ORDER_IN_FLIGHT"

	// CodeDomainOrderLimitReached is the 403 after 20 orders in 24 hours.
	CodeDomainOrderLimitReached = "DOMAIN_ORDER_LIMIT_REACHED"

	// CodeDomainNotConfigurable is the 409 for a settings change on a domain
	// that is not active.
	CodeDomainNotConfigurable = "DOMAIN_NOT_CONFIGURABLE"

	// CodeDomainZoneMismatch is the 422 when the zone's name is not the
	// domain's.
	CodeDomainZoneMismatch = "DOMAIN_ZONE_MISMATCH"
)

// Domain and domain order statuses.
const (
	DomainActive = "active"
	DomainFailed = "failed"

	DomainOrderSucceeded       = "succeeded"
	DomainOrderFailed          = "failed"
	DomainOrderAwaitingPayment = "awaiting_payment"

	NameServerModePantech = "pantech"
	NameServerModeCustom  = "custom"
)

// Domain is a domain name registered through Pantech.
type Domain struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	TLD             string     `json:"tld"`
	Status          string     `json:"status"`
	FailureCode     *string    `json:"failure_code"`
	AutoRenew       bool       `json:"auto_renew"`
	TransferLock    bool       `json:"transfer_lock"`
	Privacy         bool       `json:"privacy"`
	NameServerMode  string     `json:"name_server_mode"`
	NameServers     []string   `json:"name_servers"`
	ZoneID          *string    `json:"zone_id"`
	RegisteredAt    *time.Time `json:"registered_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
	ContactVerified bool       `json:"contact_verified"`
	SettingsPending bool       `json:"settings_pending"`
	AuthCodePending bool       `json:"auth_code_pending"`
	SuspendedReason *string    `json:"suspended_reason"`
	CreatedAt       *time.Time `json:"created_at"`
	UpdatedAt       *time.Time `json:"updated_at"`
	Version         int64      `json:"version"`
}

// DomainOrder is one paid registrar action: a registration, renewal or
// transfer. The amount is taken from credit before the registrar is called
// and returned (Refunded) when the order fails.
type DomainOrder struct {
	ID          string     `json:"id"`
	DomainID    string     `json:"domain_id"`
	DomainName  string     `json:"domain_name"`
	Kind        string     `json:"kind"`
	Years       int64      `json:"years"`
	AmountMinor int64      `json:"amount_minor"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status"`
	FailureCode *string    `json:"failure_code"`
	Refunded    bool       `json:"refunded"`
	CreatedAt   *time.Time `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`

	// While Status is awaiting_payment, PaymentExpiresAt is when the order
	// fails with insufficient_credit if it is unpaid, and PaymentURL is set
	// only when the bank asks the customer to confirm the saved-card payment
	// of the part credit does not cover (US dollars).
	PaymentURL       *string    `json:"payment_url"`
	PaymentExpiresAt *time.Time `json:"payment_expires_at"`
}

// DomainContact is the registrant. It is personal data: never log it.
type DomainContact struct {
	Type             string `json:"type"`
	FirstName        string `json:"first_name"`
	LastName         string `json:"last_name"`
	OrganizationName string `json:"organization_name"`
	Email            string `json:"email"`
	Phone            string `json:"phone"`
	AddressLine1     string `json:"address_line1"`
	AddressLine2     string `json:"address_line2"`
	City             string `json:"city"`
	State            string `json:"state"`
	CountryCode      string `json:"country_code"`
	PostalCode       string `json:"postal_code"`
}

// TLDOffer is the per-year price of one TLD, in minor units.
type TLDOffer struct {
	TLD           string `json:"tld"`
	Currency      string `json:"currency"`
	RegisterMinor int64  `json:"register_minor"`
	RenewMinor    int64  `json:"renew_minor"`
	TransferMinor int64  `json:"transfer_minor"`
	Featured      bool   `json:"featured"`

	// PrivacyAlwaysOn is true for TLDs whose registry hides every
	// registrant's contact (.ng and its second levels): privacy cannot be
	// turned off there (422 PRIVACY_ALWAYS_ON on privacy).
	PrivacyAlwaysOn bool `json:"privacy_always_on"`
}

// RegisterDomainRequest buys a domain. AcceptTerms must be true: the backend
// refuses the order otherwise. AutoRenew and Privacy are pointers so false is
// sent, not dropped.
type RegisterDomainRequest struct {
	Name        string        `json:"name"`
	Years       int64         `json:"years,omitempty"`
	Contact     DomainContact `json:"contact"`
	AutoRenew   *bool         `json:"auto_renew,omitempty"`
	Privacy     *bool         `json:"privacy,omitempty"`
	AcceptTerms bool          `json:"accept_terms"`
}

// DomainOrderResult is the 202 answer of a registration: the domain and the
// order that pays for it.
type DomainOrderResult struct {
	Domain Domain      `json:"domain"`
	Order  DomainOrder `json:"order"`
}

// UpdateDomainRequest changes a domain's settings. A nil field is left out.
type UpdateDomainRequest struct {
	AutoRenew    *bool `json:"auto_renew,omitempty"`
	TransferLock *bool `json:"transfer_lock,omitempty"`
	Privacy      *bool `json:"privacy,omitempty"`
}

// SetDomainNameServersRequest points a domain at Pantech DNS (the zone with
// its name, or ZoneID) or at 2 to 6 custom name servers.
type SetDomainNameServersRequest struct {
	UsePantechDNS bool     `json:"use_pantech_dns"`
	ZoneID        string   `json:"zone_id,omitempty"`
	NameServers   []string `json:"name_servers,omitempty"`
}

// updateDomainContactRequest wraps the contact as the endpoint expects.
type updateDomainContactRequest struct {
	Contact DomainContact `json:"contact"`
}

// DomainOrderError is returned when a domain order ends failed. The failure
// code is stable; the amount was returned to credit when Refunded is true.
type DomainOrderError struct {
	Order DomainOrder
}

// Error names the order and its failure code, to quote to support.
func (e *DomainOrderError) Error() string {
	msg := fmt.Sprintf("domain order %s (%s of %s) failed", e.Order.ID, e.Order.Kind, e.Order.DomainName)
	if e.Order.FailureCode != nil && *e.Order.FailureCode != "" {
		msg += ": " + *e.Order.FailureCode
	}
	if e.Order.Refunded {
		msg += "; the amount was returned to your credit"
	}
	return msg
}

// DomainPaymentRequiredError is returned while an order waits for its
// payment: credit did not cover it. In US dollars the saved card is charged
// the difference (confirmed at PaymentURL when the bank asks); in naira the
// customer adds credit. The order then completes by itself.
type DomainPaymentRequiredError struct {
	Order DomainOrder
}

// Error names the order and where to pay.
func (e *DomainPaymentRequiredError) Error() string {
	msg := fmt.Sprintf("domain order %s (%s of %s) is waiting for its payment: credit does not cover it", e.Order.ID, e.Order.Kind, e.Order.DomainName)
	if e.Order.PaymentURL != nil {
		msg += ". Confirm the card payment at " + *e.Order.PaymentURL
	}
	if e.Order.PaymentExpiresAt != nil {
		msg += " before " + e.Order.PaymentExpiresAt.UTC().Format(time.RFC3339)
	}
	return msg
}

// RegisterDomain places a registration order. It charges the organization's
// credit, so it matters that it is replay-safe: the backend answers a replay
// on the same Idempotency-Key with the original order, which makes a retry on
// a gateway error unable to buy twice.
func (c *Client) RegisterDomain(ctx context.Context, req RegisterDomainRequest) (*DomainOrderResult, error) {
	var res DomainOrderResult
	if err := c.do(ctx, http.MethodPost, "/domains", req, &res, replaySafe()); err != nil {
		return nil, fmt.Errorf("registering domain: %w", err)
	}
	return &res, nil
}

// GetDomain returns one domain, or ErrNotFound.
func (c *Client) GetDomain(ctx context.Context, id string) (*Domain, error) {
	var d Domain
	if err := c.do(ctx, http.MethodGet, domainPath(id), nil, &d); err != nil {
		return nil, fmt.Errorf("getting domain %s: %w", id, err)
	}
	return &d, nil
}

// ListDomains returns every domain of the organization. q, when set, is the
// backend's search filter; callers still match names exactly themselves.
func (c *Client) ListDomains(ctx context.Context, q string) ([]Domain, error) {
	path := "/domains"
	if q != "" {
		path += "?q=" + url.QueryEscape(q)
	}
	domains, err := listAll[Domain](ctx, c, path)
	if err != nil {
		return nil, fmt.Errorf("listing domains: %w", err)
	}
	return domains, nil
}

// UpdateDomain changes auto-renew, transfer lock or privacy.
func (c *Client) UpdateDomain(ctx context.Context, id string, req UpdateDomainRequest) (*Domain, error) {
	var d Domain
	if err := c.do(ctx, http.MethodPatch, domainPath(id), req, &d, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating domain %s: %w", id, err)
	}
	return &d, nil
}

// SetDomainNameServers changes where the domain is delegated. The change is
// applied at the registrar in the background (settings_pending).
func (c *Client) SetDomainNameServers(ctx context.Context, id string, req SetDomainNameServersRequest) (*Domain, error) {
	var d Domain
	if err := c.do(ctx, http.MethodPut, domainPath(id)+"/name-servers", req, &d, replaySafe()); err != nil {
		return nil, fmt.Errorf("setting name servers of domain %s: %w", id, err)
	}
	return &d, nil
}

// GetDomainContact returns the registrant. It needs an owner's or admin's
// write-scope key.
func (c *Client) GetDomainContact(ctx context.Context, id string) (*DomainContact, error) {
	var contact DomainContact
	if err := c.do(ctx, http.MethodGet, domainPath(id)+"/contact", nil, &contact); err != nil {
		return nil, fmt.Errorf("getting contact of domain %s: %w", id, err)
	}
	return &contact, nil
}

// UpdateDomainContact replaces the registrant, applied at the registrar in the
// background.
func (c *Client) UpdateDomainContact(ctx context.Context, id string, contact DomainContact) (*Domain, error) {
	var d Domain
	if err := c.do(ctx, http.MethodPut, domainPath(id)+"/contact", updateDomainContactRequest{Contact: contact}, &d, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating contact of domain %s: %w", id, err)
	}
	return &d, nil
}

// GetDomainOrder returns one order, or ErrNotFound.
func (c *Client) GetDomainOrder(ctx context.Context, id string) (*DomainOrder, error) {
	var o DomainOrder
	if err := c.do(ctx, http.MethodGet, "/domain-orders/"+url.PathEscape(id), nil, &o); err != nil {
		return nil, fmt.Errorf("getting domain order %s: %w", id, err)
	}
	return &o, nil
}

// WaitForDomainOrder polls until the order succeeds, and returns it. A failed
// order is returned as a *DomainOrderError, and one waiting for a card
// payment as a *DomainPaymentRequiredError. Callers set the deadline through
// ctx.
func (c *Client) WaitForDomainOrder(ctx context.Context, id string) (*DomainOrder, error) {
	var settled *DomainOrder
	err := c.pollUntil(ctx, "domain order "+id, func(ctx context.Context) (bool, string, error) {
		order, err := c.GetDomainOrder(ctx, id)
		if err != nil {
			return false, "", err
		}
		switch order.Status {
		case DomainOrderSucceeded:
			settled = order
			return true, order.Status, nil
		case DomainOrderFailed:
			return false, order.Status, &DomainOrderError{Order: *order}
		case DomainOrderAwaitingPayment:
			// Terraform cannot pay by card: stop and say where to pay.
			return false, order.Status, &DomainPaymentRequiredError{Order: *order}
		}
		return false, order.Status, nil
	})
	if err != nil {
		return nil, err
	}
	return settled, nil
}

// ListDomainOrders returns a domain's orders, newest first.
func (c *Client) ListDomainOrders(ctx context.Context, domainID string) ([]DomainOrder, error) {
	orders, err := listAll[DomainOrder](ctx, c, domainPath(domainID)+"/orders")
	if err != nil {
		return nil, fmt.Errorf("listing orders of domain %s: %w", domainID, err)
	}
	return orders, nil
}

// ListDomainTLDs returns every TLD Pantech sells, with its prices.
func (c *Client) ListDomainTLDs(ctx context.Context) ([]TLDOffer, error) {
	tlds, err := listAll[TLDOffer](ctx, c, "/domains/tlds")
	if err != nil {
		return nil, fmt.Errorf("listing domain tlds: %w", err)
	}
	return tlds, nil
}

// domainPath escapes the id so a malformed value cannot alter the URL path.
func domainPath(id string) string {
	return "/domains/" + url.PathEscape(id)
}
