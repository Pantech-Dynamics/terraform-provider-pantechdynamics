package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const domainJSON = `{"id":"dom_1","name":"example.com","tld":"com","status":"active","failure_code":null,"auto_renew":true,"transfer_lock":true,"privacy":true,"name_server_mode":"pantech","name_servers":["ns-1.example-dns.org"],"zone_id":"dnsz_1","registered_at":"2026-11-10T10:05:00Z","expires_at":"2027-11-10T10:05:00Z","contact_verified":false,"settings_pending":false,"auth_code_pending":false,"suspended_reason":null,"created_at":"2026-11-10T10:04:41Z","updated_at":"2026-11-10T10:05:00Z","version":4}`

const domainOrderJSON = `{"id":"dor_1","domain_id":"dom_1","domain_name":"example.com","kind":"register","years":1,"amount_minor":1500,"currency":"USD","status":"pending","failure_code":null,"refunded":false,"created_at":"2026-11-10T10:04:41Z","completed_at":null}`

func testContact() DomainContact {
	return DomainContact{Type: "person", FirstName: "Ada", LastName: "Obi", Email: "ada@example.com", Phone: "+234.8012345678", AddressLine1: "1 Marina", City: "Lagos", CountryCode: "NG"}
}

func TestRegisterDomain(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
	}{
		{"ordered", 202, `{"domain":` + strings.Replace(domainJSON, `"active"`, `"registering"`, 1) + `,"order":` + domainOrderJSON + `}`, nil},
		{"taken", 409, `{"status":409,"code":"DOMAIN_NOT_AVAILABLE"}`, func(e error) bool { return HasCode(e, CodeDomainNotAvailable) }},
		{"terms not accepted", 422, `{"status":422,"code":"VALIDATION_FAILED","errors":[{"field":"accept_terms","code":"REQUIRED","message":"must be true"}]}`, func(e error) bool { return HasFieldCode(e, "accept_terms", "REQUIRED") }},
		{"insufficient credit", 402, `{"status":402,"code":"INSUFFICIENT_CREDIT"}`, func(e error) bool { return HasCode(e, "INSUFFICIENT_CREDIT") }},
		{"malformed json", 202, `{"domain":`, func(e error) bool { return e != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/domains" || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				body, _ := io.ReadAll(r.Body)
				for _, want := range []string{`"name":"example.com"`, `"years":2`, `"accept_terms":true`, `"privacy":false`, `"country_code":"NG"`} {
					if !strings.Contains(string(body), want) {
						t.Errorf("body = %s, want %s", body, want)
					}
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			off := false
			res, err := c.RegisterDomain(context.Background(), RegisterDomainRequest{Name: "example.com", Years: 2, Contact: testContact(), Privacy: &off, AcceptTerms: true})
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || res.Domain.ID != "dom_1" || res.Order.ID != "dor_1" || res.Order.AmountMinor != 1500 {
				t.Fatalf("result = %+v, err = %v", res, err)
			}
		})
	}
}

// A registration charges credit; the backend replays it on the same key, so a
// retry after a gateway error cannot buy twice and must reuse the key.
func TestRegisterDomainRetriesGatewayErrorsWithTheSameKey(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"domain":` + domainJSON + `,"order":` + domainOrderJSON + `}`))
	})
	if _, err := c.RegisterDomain(context.Background(), RegisterDomainRequest{Name: "example.com", Contact: testContact(), AcceptTerms: true}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys = %v", keys)
	}
}

func TestGetDomain(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantNotFound bool
		wantErr      bool
	}{
		{"found", 200, domainJSON, false, false},
		{"unknown", 404, `{"status":404,"code":"DOMAIN_NOT_FOUND"}`, true, true},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, false, true},
		{"not sold here", 503, `{"status":503,"code":"DOMAINS_UNAVAILABLE"}`, false, true},
		{"malformed json", 200, `[`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/domains/dom_1" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			d, err := c.GetDomain(context.Background(), "dom_1")
			if errors.Is(err, ErrNotFound) != tt.wantNotFound || (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !tt.wantErr && (d.NameServerMode != NameServerModePantech || d.ExpiresAt == nil || *d.ZoneID != "dnsz_1") {
				t.Fatalf("domain = %+v", d)
			}
		})
	}
}

func TestWaitForDomainOrder(t *testing.T) {
	tests := []struct {
		name     string
		statuses []string
		final    string
		wantErr  string
	}{
		{"succeeds", []string{"pending", "paid", "submitted", "succeeded"}, `"failure_code":null`, ""},
		{"fails with its code", []string{"pending", "failed"}, `"failure_code":"domain_unavailable","refunded":true`, "domain_unavailable; the amount was returned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/domain-orders/dor_1" {
					t.Errorf("path = %s", r.URL.Path)
				}
				st := tt.statuses[min(i, len(tt.statuses)-1)]
				i++
				body := strings.Replace(domainOrderJSON, `"pending"`, `"`+st+`"`, 1)
				if st == "failed" || st == "succeeded" {
					body = strings.Replace(body, `"failure_code":null,"refunded":false`, tt.final, 1)
				}
				_, _ = w.Write([]byte(body))
			})
			order, err := c.WaitForDomainOrder(context.Background(), "dor_1")
			if tt.wantErr == "" {
				if err != nil || order.Status != DomainOrderSucceeded {
					t.Fatalf("order = %+v, err = %v", order, err)
				}
				return
			}
			var orderErr *DomainOrderError
			if !errors.As(err, &orderErr) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A poll that hits a gateway error does not end the wait: the order was
// already placed and paid.
func TestWaitForDomainOrderRidesOutAnOutage(t *testing.T) {
	i := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		i++
		if i <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(strings.Replace(domainOrderJSON, `"pending"`, `"succeeded"`, 1)))
	})
	if _, err := c.WaitForDomainOrder(context.Background(), "dor_1"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDomainSendsOnlySetFields(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/domains/dom_1" || string(body) != `{"transfer_lock":false}` {
			t.Errorf("got %s %s %s", r.Method, r.URL.Path, body)
		}
		_, _ = w.Write([]byte(domainJSON))
	})
	off := false
	if _, err := c.UpdateDomain(context.Background(), "dom_1", UpdateDomainRequest{TransferLock: &off}); err != nil {
		t.Fatal(err)
	}
}

func TestSetDomainNameServers(t *testing.T) {
	tests := []struct {
		name string
		req  SetDomainNameServersRequest
		want string
	}{
		{"pantech", SetDomainNameServersRequest{UsePantechDNS: true}, `{"use_pantech_dns":true}`},
		{"custom", SetDomainNameServersRequest{NameServers: []string{"ns1.example.net", "ns2.example.net"}}, `{"use_pantech_dns":false,"name_servers":["ns1.example.net","ns2.example.net"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPut || r.URL.Path != "/v1/domains/dom_1/name-servers" || string(body) != tt.want {
					t.Errorf("got %s %s %s", r.Method, r.URL.Path, body)
				}
				w.WriteHeader(202)
				_, _ = w.Write([]byte(domainJSON))
			})
			if _, err := c.SetDomainNameServers(context.Background(), "dom_1", tt.req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDomainContactRoundTrip(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/domains/dom_1/contact" {
			t.Errorf("path = %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"type":"person","first_name":"Ada","last_name":"Obi","organization_name":"","email":"ada@example.com","phone":"+234.8012345678","address_line1":"1 Marina","address_line2":"","city":"Lagos","state":"","country_code":"NG","postal_code":""}`))
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if !strings.HasPrefix(string(body), `{"contact":{"type":"person"`) {
				t.Errorf("body = %s", body)
			}
			w.WriteHeader(202)
			_, _ = w.Write([]byte(domainJSON))
		}
	})
	contact, err := c.GetDomainContact(context.Background(), "dom_1")
	if err != nil || contact.Email != "ada@example.com" {
		t.Fatalf("contact = %+v, err = %v", contact, err)
	}
	if _, err := c.UpdateDomainContact(context.Background(), "dom_1", *contact); err != nil {
		t.Fatal(err)
	}
}

func TestListDomainTLDs(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/domains/tlds" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"tld":"com","currency":"USD","register_minor":1500,"renew_minor":1600,"transfer_minor":1400,"featured":true}],"next_cursor":null}`))
	})
	tlds, err := c.ListDomainTLDs(context.Background())
	if err != nil || len(tlds) != 1 || tlds[0].RenewMinor != 1600 || !tlds[0].Featured {
		t.Fatalf("tlds = %+v, err = %v", tlds, err)
	}
}

func TestListDomainsSendsQ(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/domains" || r.URL.Query().Get("q") != "example.com" {
			t.Errorf("got %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"data":[` + domainJSON + `],"next_cursor":null}`))
	})
	domains, err := c.ListDomains(context.Background(), "example.com")
	if err != nil || len(domains) != 1 {
		t.Fatalf("domains = %+v, err = %v", domains, err)
	}
}

// An order credit does not cover waits for a card payment; the wait stops
// at once with where to pay.
func TestWaitForDomainOrderStopsForPayment(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Replace(domainOrderJSON, `"status":"pending"`, `"status":"awaiting_payment","payment_url":"https://pay.example/o/1","payment_expires_at":"2026-11-10T11:04:41Z"`, 1)))
	})
	_, err := c.WaitForDomainOrder(context.Background(), "dor_1")
	var payErr *DomainPaymentRequiredError
	if !errors.As(err, &payErr) || !strings.Contains(err.Error(), "https://pay.example/o/1") || !strings.Contains(err.Error(), "2026-11-10T11:04:41Z") {
		t.Fatalf("err = %v", err)
	}
}

func TestListDomainOrdersAndTLDPrivacy(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/domains/dom_1/orders":
			_, _ = w.Write([]byte(`{"data":[` + domainOrderJSON + `],"next_cursor":null}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"tld":"com.ng","currency":"NGN","register_minor":550000,"renew_minor":550000,"transfer_minor":550000,"featured":true,"privacy_always_on":true}],"next_cursor":null}`))
		}
	})
	if orders, err := c.ListDomainOrders(context.Background(), "dom_1"); err != nil || len(orders) != 1 {
		t.Fatalf("orders = %+v, err = %v", orders, err)
	}
	if tlds, err := c.ListDomainTLDs(context.Background()); err != nil || !tlds[0].PrivacyAlwaysOn {
		t.Fatalf("tlds = %+v, err = %v", tlds, err)
	}
}
