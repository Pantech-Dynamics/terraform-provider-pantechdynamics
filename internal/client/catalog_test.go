package client

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

const plansJSON = `{"data":[
 {"id":"plan_1","slug":"individual","name":"Individual","vcpu":1,"memory_mb":1024,"disk_gb":20,
  "price":{"currency":"NGN","monthly_estimate_minor":3760000,"storage_floor_minor":460000,"initial_payment_minor":1504000},"unpriced_reason":null},
 {"id":"plan_2","slug":"starter","name":"Starter","vcpu":1,"memory_mb":2048,"disk_gb":40,"price":null,"unpriced_reason":"NO_PRICE_FOR_CURRENCY"}
],"next_cursor":null}`

func TestListPlans(t *testing.T) {
	tests := []struct {
		name      string
		placement string
		wantQuery string
	}{
		{"default placement sends no filter", "", ""},
		{"vpc placement", "vpc", "placement=vpc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/plans" || r.URL.RawQuery != tt.wantQuery {
					t.Errorf("got %s?%s, want query %q", r.URL.Path, r.URL.RawQuery, tt.wantQuery)
				}
				if r.Header.Get("Idempotency-Key") != "" {
					t.Error("GET must not send an Idempotency-Key")
				}
				_, _ = w.Write([]byte(plansJSON))
			})

			plans, err := c.ListPlans(context.Background(), tt.placement)
			if err != nil {
				t.Fatal(err)
			}
			if len(plans) != 2 {
				t.Fatalf("plans = %+v", plans)
			}
			p := plans[0]
			if p.Slug != "individual" || p.VCPU != 1 || p.MemoryMB != 1024 || p.DiskGB != 20 ||
				p.Price == nil || p.Price.MonthlyEstimateMinor != 3760000 || p.Price.Currency != "NGN" {
				t.Fatalf("plan = %+v price = %+v", p, p.Price)
			}
			if plans[1].Price != nil || plans[1].UnpricedReason == nil || *plans[1].UnpricedReason != "NO_PRICE_FOR_CURRENCY" {
				t.Fatalf("unpriced plan = %+v", plans[1])
			}
		})
	}
}

func TestListImages(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"img_1","slug":"ubuntu-24-04","name":"Ubuntu","version":"24.04 LTS","zones":["af-abj-1","af-abj-2"]}],"next_cursor":null}`))
	})

	images, err := c.ListImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Slug != "ubuntu-24-04" || len(images[0].Zones) != 2 || images[0].Version != "24.04 LTS" {
		t.Fatalf("images = %+v", images)
	}
}

func TestListRegions(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/regions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"af-abj","name":"Abuja","placements":[
			{"kind":"standard","zone":"af-abj-1","available":true,"unavailable_reason":null},
			{"kind":"vpc","zone":"af-abj-2","available":false,"unavailable_reason":"ACCOUNT_NOT_READY"}]}],"next_cursor":null}`))
	})

	regions, err := c.ListRegions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 1 || len(regions[0].Placements) != 2 {
		t.Fatalf("regions = %+v", regions)
	}
	std, vpc := regions[0].Placements[0], regions[0].Placements[1]
	if std.Kind != PlacementStandard || !std.Available || std.UnavailableReason != nil {
		t.Fatalf("standard = %+v", std)
	}
	if vpc.Kind != PlacementVPC || vpc.Available || vpc.UnavailableReason == nil || *vpc.UnavailableReason != "ACCOUNT_NOT_READY" {
		t.Fatalf("vpc = %+v", vpc)
	}
}

func TestCatalogListsCommonBehaviour(t *testing.T) {
	calls := map[string]func(*Client) error{
		"plans":   func(c *Client) error { _, err := c.ListPlans(context.Background(), ""); return err },
		"images":  func(c *Client) error { _, err := c.ListImages(context.Background()); return err },
		"regions": func(c *Client) error { _, err := c.ListRegions(context.Background()); return err },
	}
	responses := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"empty list is not an error", 200, `{"data":[],"next_cursor":null}`, func(e error) bool { return e == nil }},
		{"missing data is an empty list", 200, `{"next_cursor":null}`, func(e error) bool { return e == nil }},
		{"unexpected cursor fails loudly", 200, `{"data":[],"next_cursor":"more"}`, func(e error) bool { return errors.Is(e, errUnexpectedPagination) }},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, func(e error) bool { return HasCode(e, "UNAUTHENTICATED") }},
		{"invalid filter", 400, `{"status":400,"code":"INVALID_FILTER","detail":"placement must be one of standard, vpc."}`, func(e error) bool { return HasCode(e, "INVALID_FILTER") }},
		{"malformed json", 200, `{not json`, func(e error) bool { return e != nil }},
	}
	for name, call := range calls {
		for _, r := range responses {
			t.Run(name+"/"+r.name, func(t *testing.T) {
				c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(r.status)
					_, _ = w.Write([]byte(r.body))
				})
				if err := call(c); !r.check(err) {
					t.Fatalf("err = %v", err)
				}
			})
		}
	}
}

func TestCatalogGetIsRetriedOn5xx(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`))
	})
	if _, err := c.ListImages(context.Background()); err != nil || calls != 2 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}
