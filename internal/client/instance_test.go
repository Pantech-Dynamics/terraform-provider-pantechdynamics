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

// instanceJSON is the live shape seen on dev, trimmed of nothing.
const instanceJSON = `{"id":"vm_1","name":"web","plan_id":"plan_1","plan_slug":"individual","image_id":"img_1","image_slug":"ubuntu-24-04","region":"af-abj","zone":"af-abj-1","network_id":null,"subnet_id":null,"security_group_id":"sg_1","public_ipv4":null,"private_ipv4":"102.211.122.77","desired_state":"running","observed_state":"running","spec":{"vcpu":1,"memory_mb":1024,"disk_gb":20},"tags":{"purpose":"tf"},"failure":null,"created_at":"2026-10-03T23:38:52.919071Z","updated_at":"2026-10-03T23:39:25.353288Z"}`

const orderRefJSON = `{"order_id":"ord_1","instance_id":"vm_1","status":"awaiting_payment","operation_id":null,"failure_code":null,"amount_minor":1504000,"currency":"NGN"}`

func TestCreateInstance(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/instances" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing Idempotency-Key")
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		for _, field := range []string{"region", "security_group_id", "ssh_key_id"} {
			if _, present := got[field]; present && field != "ssh_key_id" {
				t.Errorf("empty optional field %q must be omitted, body = %s", field, body)
			}
		}
		if got["plan_slug"] != "individual" || got["ssh_key_id"] != "sshk_1" {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(orderRefJSON))
	})

	ref, err := c.CreateInstance(context.Background(), CreateInstanceRequest{Name: "web", PlanSlug: "individual", ImageSlug: "ubuntu-24-04", SSHKeyID: "sshk_1"})
	if err != nil {
		t.Fatal(err)
	}
	if ref.OrderID != "ord_1" || ref.InstanceID != "vm_1" || ref.Status != OrderAwaitingPayment || ref.AmountMinor != 1504000 || ref.Currency != "NGN" || ref.OperationID != nil {
		t.Fatalf("ref = %+v", ref)
	}
}

// A create spends money, so these tests pin down exactly when it may be re-sent.
func TestCreateInstanceRetryIsMoneySafe(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantCalls int
	}{
		{"gateway error is retried", http.StatusBadGateway, 2},
		{"service unavailable is retried", http.StatusServiceUnavailable, 2},
		{"a plain 500 is not retried", http.StatusInternalServerError, 1},
		{"insufficient credit is not retried", http.StatusPaymentRequired, 1},
		{"validation error is not retried", http.StatusUnprocessableEntity, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var keys []string
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if len(keys) == 1 {
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(`{"status":1,"code":"X"}`))
					return
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(orderRefJSON))
			})

			_, _ = c.CreateInstance(context.Background(), CreateInstanceRequest{Name: "web", PlanSlug: "individual", ImageSlug: "ubuntu-24-04"})

			if len(keys) != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", len(keys), tt.wantCalls)
			}
			if len(keys) == 2 && keys[0] != keys[1] {
				t.Fatalf("a retried create must reuse its Idempotency-Key, got %v", keys)
			}
		})
	}
}

func TestCreateInstanceInsufficientCredit(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"status":402,"code":"INSUFFICIENT_CREDIT","detail":"This instance needs NGN 15,040.00 of credit upfront and NGN 1,000.00 is available.","request_id":"req_1"}`))
	})

	_, err := c.CreateInstance(context.Background(), CreateInstanceRequest{Name: "web", PlanSlug: "individual", ImageSlug: "ubuntu-24-04"})

	if !HasCode(err, CodeInsufficientCredit) || !strings.Contains(err.Error(), "NGN 15,040.00") {
		t.Fatalf("err = %v, want the credit detail kept", err)
	}
}

func TestGetInstance(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/instances/vm_1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(instanceJSON))
	})

	inst, err := c.GetInstance(context.Background(), "vm_1")
	if err != nil {
		t.Fatal(err)
	}
	if inst.ID != "vm_1" || inst.Zone != "af-abj-1" || inst.PlanSlug != "individual" || inst.Spec.MemoryMB != 1024 ||
		inst.PublicIPv4 != nil || inst.PrivateIPv4 == nil || *inst.PrivateIPv4 != "102.211.122.77" ||
		inst.NetworkID != nil || inst.Tags["purpose"] != "tf" || inst.Failure != nil || inst.ObservedState != InstanceRunning || inst.CreatedAt == nil {
		t.Fatalf("inst = %+v", inst)
	}
}

func TestGetInstanceNotFoundAndDeleted(t *testing.T) {
	t.Run("404 is ErrNotFound", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
		})
		if _, err := c.GetInstance(context.Background(), "vm_x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a deleted instance is a normal 200", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(instanceJSON, `"observed_state":"running"`, `"observed_state":"deleted"`), `"desired_state":"running"`, `"desired_state":"deleted"`)))
		})
		inst, err := c.GetInstance(context.Background(), "vm_1")
		if err != nil || inst.ObservedState != InstanceDeleted {
			t.Fatalf("inst = %+v, err = %v", inst, err)
		}
	})
}

func TestListInstancesFollowsCursor(t *testing.T) {
	var cursors []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		if cursor == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"vm_1","name":"a"}],"next_cursor":"p2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"vm_2","name":"b"}],"next_cursor":null}`))
	})

	instances, err := c.ListInstances(context.Background())
	if err != nil || len(instances) != 2 || instances[1].ID != "vm_2" || len(cursors) != 2 || cursors[1] != "p2" {
		t.Fatalf("instances = %+v, cursors = %v, err = %v", instances, cursors, err)
	}
}

func TestRenameInstance(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/instances/vm_1/rename" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"name":"new"`) {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(http.StatusBadGateway)
	})

	if _, err := c.RenameInstance(context.Background(), "vm_1", "new"); err == nil {
		t.Fatal("want an error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d: rename is not verified replay-safe, so it must not be retried", calls)
	}
}

func TestDeleteInstance(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
	}{
		{"accepted", 202, `{"operation_id":"op_1","resource_id":"vm_1","status":"submitting"}`, nil},
		{"never existed", 404, notFoundBody, func(e error) bool { return errors.Is(e, ErrNotFound) }},
		{"has a public ip", 409, `{"status":409,"code":"INSTANCE_HAS_PUBLIC_IP"}`, func(e error) bool { return HasCode(e, CodeInstanceHasPublicIP) }},
		{"has port forwards", 409, `{"status":409,"code":"INSTANCE_HAS_PORT_FORWARDS"}`, func(e error) bool { return HasCode(e, CodeInstanceHasPortForwards) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/instances/vm_1" || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			ref, err := c.DeleteInstance(context.Background(), "vm_1")

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

func TestDeleteInstanceRetriesGatewayErrorWithSameKey(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation_id":"op_1","resource_id":"vm_1","status":"submitting"}`))
	})
	if _, err := c.DeleteInstance(context.Background(), "vm_1"); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("keys = %v", keys)
	}
}
