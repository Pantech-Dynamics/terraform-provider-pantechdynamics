package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const dnsZoneJSON = `{"id":"dnsz_1","name":"example.com","description":"Marketing","status":"awaiting_delegation","failure_code":null,"name_servers":["ns-1.example-dns.org","ns-2.example-dns.com"],"domain_id":null,"record_count":2,"last_checked_at":null,"activated_at":null,"suspended_reason":null,"created_at":"2026-11-10T09:58:12.123456Z","updated_at":"2026-11-10T10:00:00Z","version":3}`

const dnsRecordJSON = `{"id":"dnsr_1","zone_id":"dnsz_1","name":"www","fqdn":"www.example.com","type":"A","ttl":300,"values":["203.0.113.10"],"allow_private":false,"comment":"","managed":false,"status":"pending","failure_code":null,"version":1,"created_at":"2026-11-10T10:01:00Z","updated_at":"2026-11-10T10:01:00Z"}`

func TestCreateDNSZone(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
		calls   int
	}{
		{"created", 201, dnsZoneJSON, nil, 1},
		{"name taken", 409, `{"status":409,"code":"DNS_ZONE_NAME_TAKEN"}`, func(e error) bool { return HasCode(e, CodeDNSZoneNameTaken) }, 1},
		{"invalid name", 422, `{"status":422,"code":"VALIDATION_FAILED","errors":[{"field":"name","code":"INVALID","message":"bad"}]}`, func(e error) bool { return HasFieldCode(e, "name", "INVALID") }, 1},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, func(e error) bool { return HasCode(e, "UNAUTHENTICATED") }, 1},
		{"rate limited is retried", 429, `{"status":429,"code":"RATE_LIMITED"}`, func(e error) bool { return HasCode(e, "RATE_LIMITED") }, 3},
		{"a plain 500 is not retried", 500, `{"status":500,"code":"INTERNAL"}`, func(e error) bool { return HasCode(e, "INTERNAL") }, 1},
		{"malformed json", 201, `{"id":`, func(e error) bool { return e != nil }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/dns/zones" || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"name":"example.com"}` {
					t.Errorf("body = %s, want no empty description", body)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			z, err := c.CreateDNSZone(context.Background(), CreateDNSZoneRequest{Name: "example.com"})
			if calls != tt.calls {
				t.Errorf("calls = %d, want %d", calls, tt.calls)
			}
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || z.ID != "dnsz_1" || len(z.NameServers) != 2 || z.Version != 3 || z.DomainID != nil {
				t.Fatalf("zone = %+v, err = %v", z, err)
			}
		})
	}
}

// A zone create replays on the same Idempotency-Key, so a gateway error is
// retried with the same key.
func TestCreateDNSZoneRetriesGatewayErrorsWithTheSameKey(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(dnsZoneJSON))
	})
	if _, err := c.CreateDNSZone(context.Background(), CreateDNSZoneRequest{Name: "example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys = %v, want two tries with one key", keys)
	}
}

func TestGetDNSZone(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantNotFound bool
		wantErr      bool
	}{
		{"found", 200, dnsZoneJSON, false, false},
		{"deleted", 404, `{"status":404,"code":"DNS_ZONE_NOT_FOUND","request_id":"req_1"}`, true, true},
		{"generic not found", 404, notFoundBody, true, true},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, false, true},
		{"dns not offered", 503, `{"status":503,"code":"DNS_UNAVAILABLE"}`, false, true},
		{"malformed json", 200, `{"id":`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != "/v1/dns/zones/dnsz_1" {
					t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			z, err := c.GetDNSZone(context.Background(), "dnsz_1")
			if errors.Is(err, ErrNotFound) != tt.wantNotFound || (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !tt.wantErr && z.Status != DNSZoneAwaitingDelegation {
				t.Fatalf("zone = %+v", z)
			}
		})
	}
}

func TestGetDNSZoneEscapesTheID(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1/dns/zones/a%2Fb" {
			t.Errorf("path = %s", r.URL.EscapedPath())
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(notFoundBody))
	})
	if _, err := c.GetDNSZone(context.Background(), "a/b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListDNSZonesFollowsTheCursorAndSendsQ(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/dns/zones" || r.URL.Query().Get("q") != "example.com" {
			t.Errorf("got %s", r.URL.String())
		}
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"data":[` + dnsZoneJSON + `],"next_cursor":"c2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Replace(dnsZoneJSON, "dnsz_1", "dnsz_2", 1) + `],"next_cursor":null}`))
	})
	zones, err := c.ListDNSZones(context.Background(), "example.com")
	if err != nil || len(zones) != 2 || zones[1].ID != "dnsz_2" {
		t.Fatalf("zones = %+v, err = %v", zones, err)
	}
}

func TestUpdateDNSZoneSendsTheDescription(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/dns/zones/dnsz_1" || string(body) != `{"description":""}` {
			t.Errorf("got %s %s %s", r.Method, r.URL.Path, body)
		}
		_, _ = w.Write([]byte(dnsZoneJSON))
	})
	if _, err := c.UpdateDNSZone(context.Background(), "dnsz_1", UpdateDNSZoneRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteDNSZone(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantErr      bool
		wantNotFound bool
		wantCalls    int
	}{
		{"deleting", 202, strings.Replace(dnsZoneJSON, "awaiting_delegation", "deleting", 1), false, false, 1},
		{"already gone", 404, `{"status":404,"code":"DNS_ZONE_NOT_FOUND"}`, true, true, 1},
		{"in use by a domain", 409, `{"status":409,"code":"DNS_ZONE_IN_USE_BY_DOMAIN"}`, true, false, 1},
		{"gateway error is retried", 503, "", true, false, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/dns/zones/dnsz_1" || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			_, err := c.DeleteDNSZone(context.Background(), "dnsz_1")
			if (err != nil) != tt.wantErr || errors.Is(err, ErrNotFound) != tt.wantNotFound {
				t.Fatalf("err = %v", err)
			}
			if calls != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestCreateDNSRecord(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/dns/zones/dnsz_1/records" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"name":"www","type":"A","ttl":300,"values":["203.0.113.10"]}` {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(dnsRecordJSON))
	})
	rec, err := c.CreateDNSRecord(context.Background(), "dnsz_1", CreateDNSRecordRequest{Name: "www", Type: "A", TTL: 300, Values: []string{"203.0.113.10"}})
	if err != nil || rec.ID != "dnsr_1" || rec.Status != DNSRecordPending || rec.FQDN != "www.example.com" {
		t.Fatalf("record = %+v, err = %v", rec, err)
	}
}

func TestCreateDNSRecordExists(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"status":409,"code":"DNS_RECORD_EXISTS"}`))
	})
	_, err := c.CreateDNSRecord(context.Background(), "dnsz_1", CreateDNSRecordRequest{Name: "www", Type: "A", Values: []string{"203.0.113.10"}})
	if !HasCode(err, CodeDNSRecordExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetDNSRecordNotFound(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/dns/zones/dnsz_1/records/dnsr_1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"status":404,"code":"DNS_RECORD_NOT_FOUND"}`))
	})
	if _, err := c.GetDNSRecord(context.Background(), "dnsz_1", "dnsr_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListDNSRecordsSendsTheFilter(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/dns/zones/dnsz_1/records" || q.Get("name") != "www" || q.Get("type") != "A" {
			t.Errorf("got %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"data":[` + dnsRecordJSON + `],"next_cursor":null}`))
	})
	recs, err := c.ListDNSRecords(context.Background(), "dnsz_1", DNSRecordFilter{Name: "www", Type: "A"})
	if err != nil || len(recs) != 1 || recs[0].Values[0] != "203.0.113.10" {
		t.Fatalf("records = %+v, err = %v", recs, err)
	}
}

func TestUpdateDNSRecordSendsIfMatch(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
	}{
		{"updated", 202, dnsRecordJSON, nil},
		{"version moved on", 412, `{"status":412,"code":"VERSION_CONFLICT"}`, func(e error) bool { return HasCode(e, CodeVersionConflict) }},
		{"bad value", 422, `{"status":422,"code":"VALIDATION_FAILED","errors":[{"field":"values[0]","code":"INVALID","message":"must be an IPv4 address"}]}`, func(e error) bool { return HasFieldCode(e, "values[0]", "INVALID") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPatch || r.URL.Path != "/v1/dns/zones/dnsz_1/records/dnsr_1" || r.Header.Get("If-Match") != "7" {
					t.Errorf("got %s %s If-Match=%q", r.Method, r.URL.Path, r.Header.Get("If-Match"))
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"ttl":60,"comment":""}` {
					t.Errorf("body = %s", body)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			ttl, comment := int64(60), ""
			_, err := c.UpdateDNSRecord(context.Background(), "dnsz_1", "dnsr_1", 7, UpdateDNSRecordRequest{TTL: &ttl, Comment: &comment})
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !tt.wantErr(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// Only the record update sends If-Match: other writes must not.
func TestOtherWritesSendNoIfMatch(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != "" {
			t.Errorf("If-Match = %q", r.Header.Get("If-Match"))
		}
		w.WriteHeader(202)
	})
	if err := c.DeleteDNSRecord(context.Background(), "dnsz_1", "dnsr_1"); err != nil {
		t.Fatal(err)
	}
}

func TestQuoteDNSZones(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/dns/zones/quote" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"free_zones":5,"live_zones":6,"billable_zones":1,"currency":"USD","monthly_per_zone_minor":50,"monthly_estimate_minor":50,"nominal_month_hours":730}`))
	})
	q, err := c.QuoteDNSZones(context.Background())
	if err != nil || q.FreeZones != 5 || q.MonthlyPerZoneMinor != 50 || q.Currency != "USD" {
		t.Fatalf("quote = %+v, err = %v", q, err)
	}
}
