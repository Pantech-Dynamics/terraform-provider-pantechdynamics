package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the DNS endpoints return.
const (
	// CodeDNSZoneNotFound is the 404 for a zone that is not in the
	// organization, including one already deleted. It counts as ErrNotFound.
	CodeDNSZoneNotFound = "DNS_ZONE_NOT_FOUND"

	// CodeDNSRecordNotFound is the 404 for a record set that is not in the
	// zone. It counts as ErrNotFound.
	CodeDNSRecordNotFound = "DNS_RECORD_NOT_FOUND"

	// CodeDNSUnavailable is the 503 when DNS is not offered on this deployment.
	CodeDNSUnavailable = "DNS_UNAVAILABLE"

	// CodeDNSZoneNameTaken is the 409 when the organization already has a zone
	// with this name.
	CodeDNSZoneNameTaken = "DNS_ZONE_NAME_TAKEN"

	// CodeDNSRecordExists is the 409 when the zone already has a record set
	// with this name and type.
	CodeDNSRecordExists = "DNS_RECORD_EXISTS"

	// CodeVersionConflict is the 412 when If-Match names a version that is no
	// longer current.
	CodeVersionConflict = "VERSION_CONFLICT"
)

// Zone and record statuses.
const (
	DNSZonePending            = "pending"
	DNSZoneAwaitingDelegation = "awaiting_delegation"
	DNSZoneActive             = "active"
	DNSZoneDeleting           = "deleting"
	DNSZoneDeleted            = "deleted"
	DNSZoneFailed             = "failed"

	DNSRecordPending  = "pending"
	DNSRecordActive   = "active"
	DNSRecordDeleting = "deleting"
	DNSRecordFailed   = "failed"
)

// DNSZone is a hosted zone on Pantech DNS.
type DNSZone struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	Status          string     `json:"status"`
	FailureCode     *string    `json:"failure_code"`
	NameServers     []string   `json:"name_servers"`
	DomainID        *string    `json:"domain_id"`
	RecordCount     int64      `json:"record_count"`
	LastCheckedAt   *time.Time `json:"last_checked_at"`
	ActivatedAt     *time.Time `json:"activated_at"`
	SuspendedReason *string    `json:"suspended_reason"`
	CreatedAt       *time.Time `json:"created_at"`
	UpdatedAt       *time.Time `json:"updated_at"`
	Version         int64      `json:"version"`
}

// CreateDNSZoneRequest adds a zone.
type CreateDNSZoneRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// UpdateDNSZoneRequest changes a zone's description, the only thing that can
// change.
type UpdateDNSZoneRequest struct {
	Description string `json:"description"`
}

// DNSRecord is one record set: every value of one name and type.
type DNSRecord struct {
	ID           string     `json:"id"`
	ZoneID       string     `json:"zone_id"`
	Name         string     `json:"name"`
	FQDN         string     `json:"fqdn"`
	Type         string     `json:"type"`
	TTL          int64      `json:"ttl"`
	Values       []string   `json:"values"`
	AllowPrivate bool       `json:"allow_private"`
	Comment      string     `json:"comment"`
	Managed      bool       `json:"managed"`
	Status       string     `json:"status"`
	FailureCode  *string    `json:"failure_code"`
	Version      int64      `json:"version"`
	CreatedAt    *time.Time `json:"created_at"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

// CreateDNSRecordRequest adds a record set. TTL 0 is left out, and the
// backend applies its default (300).
type CreateDNSRecordRequest struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	TTL          int64    `json:"ttl,omitempty"`
	Values       []string `json:"values"`
	AllowPrivate bool     `json:"allow_private,omitempty"`
	Comment      string   `json:"comment,omitempty"`
}

// UpdateDNSRecordRequest changes a record set. A nil field is left out and
// keeps its value; pointers let false and "" still be sent.
type UpdateDNSRecordRequest struct {
	TTL          *int64    `json:"ttl,omitempty"`
	Values       *[]string `json:"values,omitempty"`
	AllowPrivate *bool     `json:"allow_private,omitempty"`
	Comment      *string   `json:"comment,omitempty"`
}

// DNSRecordFilter narrows a record list. Empty fields are not sent.
type DNSRecordFilter struct {
	Name string
	Type string
}

// CreateDNSZone adds a zone. The backend answers a replayed create with the
// original zone, so it is retried on gateway errors.
func (c *Client) CreateDNSZone(ctx context.Context, req CreateDNSZoneRequest) (*DNSZone, error) {
	var z DNSZone
	if err := c.do(ctx, http.MethodPost, "/dns/zones", req, &z, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating dns zone: %w", err)
	}
	return &z, nil
}

// GetDNSZone returns one zone, or ErrNotFound.
func (c *Client) GetDNSZone(ctx context.Context, id string) (*DNSZone, error) {
	var z DNSZone
	if err := c.do(ctx, http.MethodGet, dnsZonePath(id), nil, &z); err != nil {
		return nil, fmt.Errorf("getting dns zone %s: %w", id, err)
	}
	return &z, nil
}

// ListDNSZones returns every zone of the organization. q, when set, is the
// backend's search filter; callers still match names exactly themselves.
func (c *Client) ListDNSZones(ctx context.Context, q string) ([]DNSZone, error) {
	path := "/dns/zones"
	if q != "" {
		path += "?q=" + url.QueryEscape(q)
	}
	zones, err := listAll[DNSZone](ctx, c, path)
	if err != nil {
		return nil, fmt.Errorf("listing dns zones: %w", err)
	}
	return zones, nil
}

// UpdateDNSZone changes a zone's description. The same change applied twice
// leaves the zone the same, so it is retried on gateway errors.
func (c *Client) UpdateDNSZone(ctx context.Context, id string, req UpdateDNSZoneRequest) (*DNSZone, error) {
	var z DNSZone
	if err := c.do(ctx, http.MethodPatch, dnsZonePath(id), req, &z, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating dns zone %s: %w", id, err)
	}
	return &z, nil
}

// DeleteDNSZone asks for the zone to be removed; it answers the zone in the
// deleting state. It returns ErrNotFound when the zone is already gone, which
// callers treat as success. Every write but the ssh-key ones replays on the
// same Idempotency-Key, so it is retried on gateway errors.
func (c *Client) DeleteDNSZone(ctx context.Context, id string) (*DNSZone, error) {
	var z DNSZone
	if err := c.do(ctx, http.MethodDelete, dnsZonePath(id), nil, &z, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting dns zone %s: %w", id, err)
	}
	return &z, nil
}

// CreateDNSRecord adds a record set. It answers 202 with the record pending.
// A replayed create answers the original, so it is retried on gateway errors.
func (c *Client) CreateDNSRecord(ctx context.Context, zoneID string, req CreateDNSRecordRequest) (*DNSRecord, error) {
	var r DNSRecord
	if err := c.do(ctx, http.MethodPost, dnsZonePath(zoneID)+"/records", req, &r, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating dns record in zone %s: %w", zoneID, err)
	}
	return &r, nil
}

// GetDNSRecord returns one record set, or ErrNotFound when it or its zone is
// gone.
func (c *Client) GetDNSRecord(ctx context.Context, zoneID, id string) (*DNSRecord, error) {
	var r DNSRecord
	if err := c.do(ctx, http.MethodGet, dnsRecordPath(zoneID, id), nil, &r); err != nil {
		return nil, fmt.Errorf("getting dns record %s: %w", id, err)
	}
	return &r, nil
}

// ListDNSRecords returns the zone's record sets, by name then type, narrowed
// by filter.
func (c *Client) ListDNSRecords(ctx context.Context, zoneID string, filter DNSRecordFilter) ([]DNSRecord, error) {
	q := url.Values{}
	if filter.Name != "" {
		q.Set("name", filter.Name)
	}
	if filter.Type != "" {
		q.Set("type", filter.Type)
	}
	path := dnsZonePath(zoneID) + "/records"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	records, err := listAll[DNSRecord](ctx, c, path)
	if err != nil {
		return nil, fmt.Errorf("listing dns records of zone %s: %w", zoneID, err)
	}
	return records, nil
}

// UpdateDNSRecord changes a record set, only if it is still at version (sent
// as If-Match); otherwise the backend answers 412 VERSION_CONFLICT. A retry
// carries the same Idempotency-Key, which the backend answers with the
// original result, so it is retried on gateway errors.
func (c *Client) UpdateDNSRecord(ctx context.Context, zoneID, id string, version int64, req UpdateDNSRecordRequest) (*DNSRecord, error) {
	var r DNSRecord
	if err := c.do(ctx, http.MethodPatch, dnsRecordPath(zoneID, id), req, &r, replaySafe(), ifMatch(version)); err != nil {
		return nil, fmt.Errorf("updating dns record %s: %w", id, err)
	}
	return &r, nil
}

// DeleteDNSRecord asks for a record set to be removed. It returns ErrNotFound
// when it is already gone, which callers treat as success. It replays on the
// same Idempotency-Key, so it is retried on gateway errors.
func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, id string) error {
	if err := c.do(ctx, http.MethodDelete, dnsRecordPath(zoneID, id), nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("deleting dns record %s: %w", id, err)
	}
	return nil
}

// DNSZoneQuote is what hosted zones cost: the first FreeZones live zones are
// free, each further one costs MonthlyPerZoneMinor a month.
type DNSZoneQuote struct {
	FreeZones            int64  `json:"free_zones"`
	LiveZones            int64  `json:"live_zones"`
	BillableZones        int64  `json:"billable_zones"`
	Currency             string `json:"currency"`
	MonthlyPerZoneMinor  int64  `json:"monthly_per_zone_minor"`
	MonthlyEstimateMinor int64  `json:"monthly_estimate_minor"`
}

// QuoteDNSZones returns what the organization's zones cost.
func (c *Client) QuoteDNSZones(ctx context.Context) (*DNSZoneQuote, error) {
	var q DNSZoneQuote
	if err := c.do(ctx, http.MethodGet, "/dns/zones/quote", nil, &q); err != nil {
		return nil, fmt.Errorf("quoting dns zones: %w", err)
	}
	return &q, nil
}

// dnsZonePath escapes the id so a malformed value cannot alter the URL path.
func dnsZonePath(id string) string {
	return "/dns/zones/" + url.PathEscape(id)
}

// dnsRecordPath escapes both ids so a malformed value cannot alter the path.
func dnsRecordPath(zoneID, id string) string {
	return dnsZonePath(zoneID) + "/records/" + url.PathEscape(id)
}
