package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestVPCWritesUseTheRightRequest(t *testing.T) {
	port := int64(22)
	tests := []struct {
		name       string
		call       func(*Client) error
		wantMethod string
		wantPath   string
		wantBody   string // substring; empty means no check
	}{
		{"create network", func(c *Client) error {
			_, err := c.CreateNetwork(context.Background(), CreateNetworkRequest{Name: "n", CIDR: "10.0.0.0/16"})
			return err
		}, http.MethodPost, "/v1/networks", `"cidr":"10.0.0.0/16"`},
		{"delete network", func(c *Client) error {
			_, err := c.DeleteNetwork(context.Background(), "net_1")
			return err
		}, http.MethodDelete, "/v1/networks/net_1", ""},
		{"create subnet", func(c *Client) error {
			_, err := c.CreateSubnet(context.Background(), "net_1", CreateSubnetRequest{Name: "s", CIDR: "10.0.1.0/24"})
			return err
		}, http.MethodPost, "/v1/networks/net_1/subnets", `"name":"s"`},
		{"delete subnet", func(c *Client) error {
			_, err := c.DeleteSubnet(context.Background(), "snet_1")
			return err
		}, http.MethodDelete, "/v1/subnets/snet_1", ""},
		{"create firewall rule", func(c *Client) error {
			_, err := c.CreateFirewallRule(context.Background(), "snet_1", CreateFirewallRuleRequest{Number: 100, Protocol: "tcp", PortStart: &port, CIDR: "0.0.0.0/0"})
			return err
		}, http.MethodPost, "/v1/subnets/snet_1/firewall-rules", `"port_start":22`},
		{"delete firewall rule", func(c *Client) error {
			_, err := c.DeleteFirewallRule(context.Background(), "aclr_1")
			return err
		}, http.MethodDelete, "/v1/firewall-rules/aclr_1", ""},
		{"create public ip", func(c *Client) error {
			_, err := c.CreatePublicIP(context.Background(), CreatePublicIPRequest{NetworkID: "net_1", Purpose: PublicIPStaticNAT, InstanceID: "vm_1"})
			return err
		}, http.MethodPost, "/v1/public-ips", `"instance_id":"vm_1"`},
		{"delete public ip", func(c *Client) error {
			_, err := c.DeletePublicIP(context.Background(), "pip_1")
			return err
		}, http.MethodDelete, "/v1/public-ips/pip_1", ""},
		{"reserve public ip without an instance", func(c *Client) error {
			_, err := c.CreatePublicIP(context.Background(), CreatePublicIPRequest{NetworkID: "net_1", Purpose: PublicIPStaticNAT})
			return err
		}, http.MethodPost, "/v1/public-ips", `{"network_id":"net_1","purpose":"static_nat"}`},
		{"attach public ip", func(c *Client) error {
			_, err := c.AttachPublicIP(context.Background(), "pip_1", "vm_2")
			return err
		}, http.MethodPost, "/v1/public-ips/pip_1/attach", `{"instance_id":"vm_2"}`},
		{"detach public ip", func(c *Client) error {
			_, err := c.DetachPublicIP(context.Background(), "pip_1")
			return err
		}, http.MethodPost, "/v1/public-ips/pip_1/detach", ""},
		{"create port forwarding rule", func(c *Client) error {
			_, err := c.CreatePortForwardingRule(context.Background(), "pip_1", CreatePortForwardingRuleRequest{InstanceID: "vm_1", PublicPortStart: 2222})
			return err
		}, http.MethodPost, "/v1/public-ips/pip_1/port-forwarding-rules", `"public_port_start":2222`},
		{"delete port forwarding rule", func(c *Client) error {
			_, err := c.DeletePortForwardingRule(context.Background(), "pfr_1")
			return err
		}, http.MethodDelete, "/v1/port-forwarding-rules/pfr_1", ""},
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
				if tt.wantBody != "" && !strings.Contains(string(body), tt.wantBody) {
					t.Errorf("body = %s, want it to contain %s", body, tt.wantBody)
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

func TestVPCReadsDecodeAndMapNotFound(t *testing.T) {
	bodies := map[string]string{
		"/v1/networks/net_1":     `{"id":"net_1","name":"n","cidr":"10.0.0.0/16","region":"af-abj","zone":"af-abj-2","instance_count":2,"observed_state":"active"}`,
		"/v1/subnets/snet_1":     `{"id":"snet_1","network_id":"net_1","name":"s","cidr":"10.0.1.0/24","observed_state":"active"}`,
		"/v1/public-ips/pip_1":   `{"id":"pip_1","network_id":"net_1","purpose":"static_nat","instance_id":"vm_1","address":"203.0.113.9","observed_state":"active"}`,
		"/v1/public-ips/pip_404": notFoundBody,
	}
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok || strings.HasSuffix(r.URL.Path, "404") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	ctx := context.Background()

	n, err := c.GetNetwork(ctx, "net_1")
	if err != nil || n.Zone != "af-abj-2" || n.InstanceCount == nil || *n.InstanceCount != 2 {
		t.Fatalf("network = %+v, err %v", n, err)
	}
	s, err := c.GetSubnet(ctx, "snet_1")
	if err != nil || s.NetworkID != "net_1" {
		t.Fatalf("subnet = %+v, err %v", s, err)
	}
	ip, err := c.GetPublicIP(ctx, "pip_1")
	if err != nil || ip.Address == nil || *ip.Address != "203.0.113.9" {
		t.Fatalf("public ip = %+v, err %v", ip, err)
	}
	if _, err := c.GetPublicIP(ctx, "pip_404"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := c.GetNetwork(ctx, "net_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRuleReadsUseTheSingleRuleEndpoints(t *testing.T) {
	const rule = `{"id":"aclr_1","subnet_id":"snet_1","number":100,"direction":"ingress","protocol":"tcp","port_start":22,"port_end":22,"cidr":"0.0.0.0/0","action":"allow"}`
	const fwd = `{"id":"pfr_1","public_ip_id":"pip_1","instance_id":"vm_1","instance_name":"web","protocol":"tcp","public_port_start":2222,"public_port_end":2222,"private_port_start":22,"private_port_end":22}`
	bodies := map[string]string{
		"/v1/subnets/snet_1/firewall-rules/aclr_1":         rule,
		"/v1/public-ips/pip_1/port-forwarding-rules/pfr_1": fwd,
	}
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	ctx := context.Background()

	fr, err := c.GetFirewallRule(ctx, "snet_1", "aclr_1")
	if err != nil || fr.Number != 100 || fr.PortStart == nil || *fr.PortStart != 22 {
		t.Fatalf("firewall rule = %+v, err %v", fr, err)
	}
	if _, err := c.GetFirewallRule(ctx, "snet_1", "aclr_other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	pf, err := c.GetPortForwardingRule(ctx, "pip_1", "pfr_1")
	if err != nil || pf.PrivatePortStart != 22 || pf.InstanceName == nil || *pf.InstanceName != "web" {
		t.Fatalf("port forward = %+v, err %v", pf, err)
	}
	if _, err := c.GetPortForwardingRule(ctx, "pip_1", "pfr_other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRuleListsFollowTheCursorAndSkipSystemRules(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/firewall-rules") && r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"data":[{"id":"aclr_1"}],"next_cursor":"c2","system_rules":[{"number":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/firewall-rules"):
			_, _ = w.Write([]byte(`{"data":[{"id":"aclr_2"}],"next_cursor":null,"system_rules":[]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"id":"pfr_1"},{"id":"pfr_2"}],"next_cursor":null}`))
		}
	})
	ctx := context.Background()
	rules, err := c.ListFirewallRules(ctx, "snet_1")
	if err != nil || len(rules) != 2 || rules[1].ID != "aclr_2" {
		t.Fatalf("rules = %+v, err %v", rules, err)
	}
	fwds, err := c.ListPortForwardingRules(ctx, "pip_1")
	if err != nil || len(fwds) != 2 {
		t.Fatalf("forwards = %+v, err %v", fwds, err)
	}
}

func TestListNetworksFollowsTheCursor(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/networks" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"net_1","name":"main"}],"next_cursor":"p2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"net_2","name":"db"}],"next_cursor":null}`))
	})
	nets, err := c.ListNetworks(context.Background())
	if err != nil || len(nets) != 2 || nets[1].Name != "db" {
		t.Fatalf("networks = %+v, err %v", nets, err)
	}
}

func TestPublicIPReadsZoneAndNames(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"pip_1","network_id":"net_1","network_name":"main","purpose":"static_nat","instance_id":"vm_1","instance_name":"web","region":"af-abj","zone":"af-abj-2","desired_state":"present","observed_state":"active"}`))
	})
	ip, err := c.GetPublicIP(context.Background(), "pip_1")
	if err != nil {
		t.Fatal(err)
	}
	if derefString(ip.Zone) != "af-abj-2" || derefString(ip.NetworkName) != "main" || derefString(ip.InstanceName) != "web" {
		t.Errorf("public ip = %+v", ip)
	}
}

func TestPublicIPReadsADetachedAddress(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"pip_1","network_id":"net_1","purpose":"static_nat","instance_id":null,"instance_name":null,"address":"203.0.113.9","desired_state":"present","observed_state":"active","in_sync":false}`))
	})
	ip, err := c.GetPublicIP(context.Background(), "pip_1")
	if err != nil {
		t.Fatal(err)
	}
	if ip.InstanceID != nil || ip.InSync {
		t.Errorf("public ip = %+v, want no instance and in_sync false", ip)
	}
}

func TestAttachWithNothingToChangeReturnsAnEmptyOperation(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation_id":"","resource_id":"pip_1","status":"succeeded"}`))
	})
	ref, err := c.AttachPublicIP(context.Background(), "pip_1", "vm_1")
	if err != nil || ref.OperationID != "" || ref.ResourceID != "pip_1" {
		t.Fatalf("ref = %+v, err %v", ref, err)
	}
}

func TestAttachRefusalsKeepTheirCodes(t *testing.T) {
	for _, code := range []string{CodePublicIPNotStaticNAT, CodeInstanceAlreadyHasPublicIP} {
		t.Run(code, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"status":409,"code":"` + code + `"}`))
			})
			if _, err := c.AttachPublicIP(context.Background(), "pip_1", "vm_1"); !HasCode(err, code) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestFirewallRuleRequestOmitsUnsetFields(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, bad := range []string{"port_start", "icmp_type", "direction", "action"} {
			if strings.Contains(string(body), bad) {
				t.Errorf("body %s contains %s, want it omitted", body, bad)
			}
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(opRefJSON))
	})
	_, err := c.CreateFirewallRule(context.Background(), "snet_1", CreateFirewallRuleRequest{Number: 200, Protocol: "all", CIDR: "0.0.0.0/0"})
	if err != nil {
		t.Fatal(err)
	}
}
