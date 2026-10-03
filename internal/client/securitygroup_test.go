package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const sgJSON = `{"id":"sg_1","name":"web","rules":[{"direction":"ingress","protocol":"tcp","port_range":"22","cidr":"10.0.0.0/8"},{"direction":"ingress","protocol":"icmp","port_range":"","cidr":"0.0.0.0/0"}],"desired_state":"present","observed_state":"active","created_at":"2026-10-03T22:57:03.517994Z","updated_at":"2026-10-03T22:58:41.943319Z"}`

const opRefJSON = `{"operation_id":"op_1","resource_id":"sg_1","status":"submitting"}`

var testRules = []SecurityGroupRule{
	{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/8"},
	{Direction: "ingress", Protocol: "icmp", PortRange: "", CIDR: "0.0.0.0/0"},
}

func TestCreateSecurityGroup(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/security-groups" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing Idempotency-Key")
		}
		body, _ := io.ReadAll(r.Body)
		// An empty port_range must be sent, not omitted.
		if !strings.Contains(string(body), `"port_range":""`) {
			t.Errorf("body = %s, want an explicit empty port_range", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(opRefJSON))
	})

	ref, err := c.CreateSecurityGroup(context.Background(), CreateSecurityGroupRequest{Name: "web", Rules: testRules})
	if err != nil {
		t.Fatal(err)
	}
	if ref.OperationID != "op_1" || ref.ResourceID != "sg_1" || ref.Status != OperationSubmitting {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestSecurityGroupWritesRetryGatewayErrorsWithSameKey(t *testing.T) {
	calls := map[string]func(*Client) error{
		"create": func(c *Client) error {
			_, err := c.CreateSecurityGroup(context.Background(), CreateSecurityGroupRequest{Name: "web", Rules: testRules})
			return err
		},
		"replace rules": func(c *Client) error {
			_, err := c.ReplaceSecurityGroupRules(context.Background(), "sg_1", testRules)
			return err
		},
		"delete": func(c *Client) error {
			_, err := c.DeleteSecurityGroup(context.Background(), "sg_1")
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			var keys []string
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if len(keys) == 1 {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(opRefJSON))
			})
			if err := call(c); err != nil {
				t.Fatal(err)
			}
			if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
				t.Fatalf("keys = %v, want the same key on the retry", keys)
			}
		})
	}
}

func TestSecurityGroupWritesDoNotRetryPlain500(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":500,"code":"INTERNAL","request_id":"req_1"}`))
	})

	_, err := c.CreateSecurityGroup(context.Background(), CreateSecurityGroupRequest{Name: "dup", Rules: testRules})

	if !HasCode(err, "INTERNAL") || calls != 1 {
		t.Fatalf("err = %v, calls = %d: a 500 (the backend's answer to a duplicate name) must not be retried", err, calls)
	}
}

func TestGetSecurityGroup(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantNotFound bool
	}{
		{"found", 200, sgJSON, false},
		{"not found", 404, notFoundBody, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/security-groups/sg_1" {
					t.Errorf("path = %q", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			sg, err := c.GetSecurityGroup(context.Background(), "sg_1")

			if tt.wantNotFound {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || sg.Name != "web" || len(sg.Rules) != 2 || sg.Rules[1].PortRange != "" || sg.ObservedState != "active" || sg.UpdatedAt == nil {
				t.Fatalf("sg = %+v, err = %v", sg, err)
			}
		})
	}
}

func TestListSecurityGroupsFollowsCursor(t *testing.T) {
	var cursors []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		if cursor == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"sg_1","name":"a","rules":[]}],"next_cursor":"p2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"sg_2","name":"b","rules":[]}],"next_cursor":null}`))
	})

	groups, err := c.ListSecurityGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[1].ID != "sg_2" || len(cursors) != 2 || cursors[1] != "p2" {
		t.Fatalf("groups = %+v, cursors = %v", groups, cursors)
	}
}

func TestReplaceSecurityGroupRules(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/security-groups/sg_1/rules" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Rules []SecurityGroupRule `json:"rules"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Rules) != 2 {
			t.Errorf("body rules = %+v, err = %v", body.Rules, err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(opRefJSON))
	})

	ref, err := c.ReplaceSecurityGroupRules(context.Background(), "sg_1", testRules)
	if err != nil || ref.OperationID != "op_1" {
		t.Fatalf("ref = %+v, err = %v", ref, err)
	}
}

func TestDeleteSecurityGroup(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
	}{
		{"accepted", 202, opRefJSON, nil},
		{"already gone", 404, notFoundBody, func(e error) bool { return errors.Is(e, ErrNotFound) }},
		{"in use", 409, `{"status":409,"code":"INVALID_RESOURCE_STATE"}`, func(e error) bool { return HasCode(e, CodeInvalidResourceState) }},
		{"default group", 409, `{"status":409,"code":"DEFAULT_SECURITY_GROUP_UNDELETABLE"}`, func(e error) bool { return HasCode(e, CodeDefaultSecurityGroupUndeletable) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/security-groups/sg_1" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			ref, err := c.DeleteSecurityGroup(context.Background(), "sg_1")

			if tt.wantErr == nil {
				if err != nil || ref.OperationID != "op_1" {
					t.Fatalf("ref = %+v, err = %v", ref, err)
				}
				return
			}
			if err == nil || !tt.wantErr(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
