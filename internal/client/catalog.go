package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Placement kinds accepted by the plans filter and reported by regions.
const (
	PlacementStandard = "standard"
	PlacementVPC      = "vpc"
)

// errUnexpectedPagination means a catalog list returned a cursor. The backend
// documents these lists as unpaginated, so a cursor would mean entries are
// missing. Failing loudly beats silently returning a truncated catalog.
var errUnexpectedPagination = errors.New("catalog list returned a next_cursor, but catalog lists are documented as unpaginated")

// PlanPrice is a plan's price in the account's currency, in minor units. It is
// an estimate from a display-only cache, not a quote.
type PlanPrice struct {
	Currency             string `json:"currency"`
	MonthlyEstimateMinor int64  `json:"monthly_estimate_minor"`
	StorageFloorMinor    int64  `json:"storage_floor_minor"`
	InitialPaymentMinor  int64  `json:"initial_payment_minor"`
}

// Plan is a compute size a customer can choose for an instance.
type Plan struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	VCPU     int64  `json:"vcpu"`
	MemoryMB int64  `json:"memory_mb"`
	DiskGB   int64  `json:"disk_gb"`

	// Price is nil when the backend cannot price the plan; UnpricedReason says why.
	Price          *PlanPrice `json:"price"`
	UnpricedReason *string    `json:"unpriced_reason"`
}

// Image is an operating system a customer can choose for an instance.
type Image struct {
	ID      string   `json:"id"`
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Zones   []string `json:"zones"`
}

// Placement is one kind of instance a region can host, and the zone it lands in.
type Placement struct {
	Kind              string  `json:"kind"`
	Zone              string  `json:"zone"`
	Available         bool    `json:"available"`
	UnavailableReason *string `json:"unavailable_reason"`
}

// Region is a location, with where each kind of instance would land.
type Region struct {
	Code       string      `json:"code"`
	Name       string      `json:"name"`
	Placements []Placement `json:"placements"`
}

// DiskOffering is a kind of volume a customer can order. A fixed offering has its
// own SizeGB. A customized one (CustomSize) takes the size from the order.
type DiskOffering struct {
	Slug             string `json:"slug"`
	Name             string `json:"name"`
	SizeGB           *int64 `json:"size_gb"`
	CustomSize       bool   `json:"custom_size"`
	StorageType      string `json:"storage_type"`
	Currency         string `json:"currency"`
	HourlyPriceMinor int64  `json:"hourly_price_minor"`
}

// catalogPage is the envelope shared by the catalog lists.
type catalogPage[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

// ListPlans returns every plan. placement is "standard" or "vpc"; empty uses
// the backend default (standard). Prices differ by placement.
func (c *Client) ListPlans(ctx context.Context, placement string) ([]Plan, error) {
	path := "/plans"
	if placement != "" {
		path += "?placement=" + url.QueryEscape(placement)
	}
	plans, err := getCatalog[Plan](ctx, c, path)
	if err != nil {
		return nil, fmt.Errorf("listing plans: %w", err)
	}
	return plans, nil
}

// ListImages returns every image.
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	images, err := getCatalog[Image](ctx, c, "/images")
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}
	return images, nil
}

// ListRegions returns every region with its placements.
func (c *Client) ListRegions(ctx context.Context) ([]Region, error) {
	regions, err := getCatalog[Region](ctx, c, "/regions")
	if err != nil {
		return nil, fmt.Errorf("listing regions: %w", err)
	}
	return regions, nil
}

// ListDiskOfferings returns every disk offering.
func (c *Client) ListDiskOfferings(ctx context.Context) ([]DiskOffering, error) {
	offerings, err := getCatalog[DiskOffering](ctx, c, "/disk-offerings")
	if err != nil {
		return nil, fmt.Errorf("listing disk offerings: %w", err)
	}
	return offerings, nil
}

// getCatalog fetches one unpaginated catalog list. The generic only removes the
// envelope handling that all three lists share.
func getCatalog[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var page catalogPage[T]
	if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	if page.NextCursor != nil && *page.NextCursor != "" {
		return nil, errUnexpectedPagination
	}
	return page.Data, nil
}
