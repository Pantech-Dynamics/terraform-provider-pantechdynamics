package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const lbJSON = `{"id":"lb_1","name":"web","public_ip_id":"pip_1","public_ip_address":"203.0.113.9","network_id":"net_1","subnet_id":"snet_1","protocol":"tcp","algorithm":"roundrobin","public_port":80,"private_port":8080,"cidr_list":["0.0.0.0/0"],"members":[{"instance_id":"vm_1","instance_name":"web-1","desired_state":"present","observed_state":"active"}],"desired_state":"present","observed_state":"active","in_sync":true,"created_at":"2026-10-07T10:00:00Z","updated_at":"2026-10-07T10:00:00Z"}`

func TestLoadBalancerWritesUseTheRightRequest(t *testing.T) {
	port := int64(8080)
	name := "api"
	empty := []string{}
	tests := []struct {
		name       string
		call       func(*Client) error
		wantMethod string
		wantPath   string
		wantBody   string // exact JSON; empty means no body check
	}{
		{"create with defaults omitted", func(c *Client) error {
			_, err := c.CreateLoadBalancer(context.Background(), CreateLoadBalancerRequest{Name: "web", PublicIPID: "pip_1", SubnetID: "snet_1", PublicPort: 80})
			return err
		}, http.MethodPost, "/v1/load-balancers", `{"name":"web","public_ip_id":"pip_1","subnet_id":"snet_1","public_port":80}`},
		{"create with everything", func(c *Client) error {
			_, err := c.CreateLoadBalancer(context.Background(), CreateLoadBalancerRequest{Name: "web", PublicIPID: "pip_1", SubnetID: "snet_1", Algorithm: "source", PublicPort: 80, PrivatePort: &port, CIDRList: []string{"10.0.0.0/8"}, InstanceIDs: []string{"vm_1"}})
			return err
		}, http.MethodPost, "/v1/load-balancers", `{"name":"web","public_ip_id":"pip_1","subnet_id":"snet_1","algorithm":"source","public_port":80,"private_port":8080,"cidr_list":["10.0.0.0/8"],"instance_ids":["vm_1"]}`},
		{"update name only", func(c *Client) error {
			_, err := c.UpdateLoadBalancer(context.Background(), "lb_1", UpdateLoadBalancerRequest{Name: &name})
			return err
		}, http.MethodPatch, "/v1/load-balancers/lb_1", `{"name":"api"}`},
		{"update to no targets sends an empty list", func(c *Client) error {
			_, err := c.UpdateLoadBalancer(context.Background(), "lb_1", UpdateLoadBalancerRequest{InstanceIDs: &empty})
			return err
		}, http.MethodPatch, "/v1/load-balancers/lb_1", `{"instance_ids":[]}`},
		{"delete", func(c *Client) error {
			_, err := c.DeleteLoadBalancer(context.Background(), "lb_1")
			return err
		}, http.MethodDelete, "/v1/load-balancers/lb_1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if r.Method != tt.wantMethod || r.URL.Path != tt.wantPath {
					t.Errorf("got %s %s, want %s %s", r.Method, r.URL.Path, tt.wantMethod, tt.wantPath)
				}
				if r.Header.Get("Idempotency-Key") == "" {
					t.Error("missing Idempotency-Key")
				}
				body, _ := io.ReadAll(r.Body)
				if tt.wantBody != "" && string(body) != tt.wantBody {
					t.Errorf("body = %s, want %s", body, tt.wantBody)
				}
				if attempts == 1 {
					w.WriteHeader(http.StatusBadGateway) // replay-safe: retried once
					return
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(opRefJSON))
			})
			if err := tt.call(c); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 {
				t.Errorf("attempts = %d, want 2 (one 502, then success)", attempts)
			}
		})
	}
}

func TestLoadBalancerReads(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/load-balancers/lb_1":
			_, _ = w.Write([]byte(lbJSON))
		case r.URL.Path == "/v1/load-balancers" && r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"data":[` + lbJSON + `],"next_cursor":"c2"}`))
		case r.URL.Path == "/v1/load-balancers":
			_, _ = w.Write([]byte(`{"data":[{"id":"lb_2","name":"db"}],"next_cursor":null}`))
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
		}
	})
	ctx := context.Background()

	lb, err := c.GetLoadBalancer(ctx, "lb_1")
	if err != nil {
		t.Fatal(err)
	}
	if lb.PublicIPAddress == nil || *lb.PublicIPAddress != "203.0.113.9" || lb.PrivatePort != 8080 || !lb.InSync ||
		len(lb.Members) != 1 || lb.Members[0].InstanceID != "vm_1" || len(lb.CIDRList) != 1 {
		t.Errorf("load balancer = %+v", lb)
	}
	if _, err := c.GetLoadBalancer(ctx, "lb_404"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	all, err := c.ListLoadBalancers(ctx)
	if err != nil || len(all) != 2 || all[1].ID != "lb_2" {
		t.Fatalf("list = %+v, err %v", all, err)
	}
}

func TestLoadBalancerRefusalsKeepTheirCode(t *testing.T) {
	for _, code := range []string{CodePublicIPNotForLoadBalancer, CodePortInUse, CodeLoadBalancerNotChangeable} {
		t.Run(code, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"status":409,"code":"` + code + `","detail":"refused"}`))
			})
			_, err := c.CreateLoadBalancer(context.Background(), CreateLoadBalancerRequest{Name: "web", PublicIPID: "pip_1", SubnetID: "snet_1", PublicPort: 80})
			if !HasCode(err, code) || !strings.Contains(err.Error(), "creating load balancer") {
				t.Errorf("err = %v, want code %s", err, code)
			}
		})
	}
}
