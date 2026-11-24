package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

const dynamicIPSampleJSON = `{
	"instance_id": "vm_1",
	"schedule": "daily",
	"connections": "keep",
	"on_demand": true,
	"state": "active",
	"current_address": "102.211.122.90",
	"next_rotation_at": "2026-11-25T12:00:00Z",
	"last_rotated_at": "2026-11-24T12:00:00Z",
	"surcharge_percent": 50,
	"on_demand_used": 2,
	"on_demand_limit": 10,
	"failure_reason": null,
	"events": [
		{
			"kind": "enabled",
			"from_address": null,
			"to_address": "102.211.122.90",
			"degraded": false,
			"occurred_at": "2026-11-24T12:00:00Z"
		}
	]
}`

func TestGetDynamicIP(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/instances/vm_1/address-rotation" {
				t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(dynamicIPSampleJSON))
		})

		got, err := c.GetDynamicIP(ctx, "vm_1")
		if err != nil {
			t.Fatalf("GetDynamicIP: %v", err)
		}
		if got.InstanceID != "vm_1" || got.Schedule != DynamicIPScheduleDaily || got.Connections != DynamicIPConnectionsKeep ||
			!got.OnDemand || got.State != DynamicIPStateActive || got.SurchargePercent != 50 ||
			got.OnDemandUsed != 2 || got.OnDemandLimit != 10 {
			t.Fatalf("got = %+v", got)
		}
		if got.CurrentAddress == nil || *got.CurrentAddress != "102.211.122.90" {
			t.Fatalf("current_address = %v", got.CurrentAddress)
		}
		if len(got.Events) != 1 || got.Events[0].Kind != "enabled" || got.Events[0].ToAddress == nil || *got.Events[0].ToAddress != "102.211.122.90" {
			t.Fatalf("events = %+v", got.Events)
		}
	})

	t.Run("not found", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"code":"ADDRESS_ROTATION_NOT_FOUND","detail":"Dynamic IP is not enabled.","request_id":"req_1"}`))
		})
		_, err := c.GetDynamicIP(ctx, "vm_1")
		if !errors.Is(err, ErrNotFound) || !HasCode(err, CodeAddressRotationNotFound) {
			t.Fatalf("err = %v, want ErrNotFound with ADDRESS_ROTATION_NOT_FOUND", err)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{bad`))
		})
		if _, err := c.GetDynamicIP(ctx, "vm_1"); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestPutDynamicIP(t *testing.T) {
	ctx := context.Background()

	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var gotBody PutDynamicIPRequest
			var gotKey string
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/v1/instances/vm_1/address-rotation" {
					t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
				}
				gotKey = r.Header.Get("Idempotency-Key")
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(dynamicIPSampleJSON))
			})

			onDemand := false
			got, err := c.PutDynamicIP(ctx, "vm_1", PutDynamicIPRequest{
				Schedule:    DynamicIPScheduleWeekly,
				Connections: DynamicIPConnectionsMove,
				OnDemand:    &onDemand,
			})
			if err != nil {
				t.Fatalf("PutDynamicIP: %v", err)
			}
			if gotKey == "" {
				t.Fatal("missing Idempotency-Key")
			}
			if gotBody.Schedule != DynamicIPScheduleWeekly || gotBody.Connections != DynamicIPConnectionsMove || gotBody.OnDemand == nil || *gotBody.OnDemand {
				t.Fatalf("sent body = %+v", gotBody)
			}
			if got.InstanceID != "vm_1" {
				t.Fatalf("got = %+v", got)
			}
		})
	}

	t.Run("problem code", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":409,"code":"ADDRESS_ROTATION_NEEDS_SECURITY_GROUP","detail":"narrow rules","request_id":"req_2"}`))
		})
		_, err := c.PutDynamicIP(ctx, "vm_1", PutDynamicIPRequest{Schedule: DynamicIPScheduleDaily})
		if !HasCode(err, CodeAddressRotationNeedsSecurityGroup) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDeleteDynamicIP(t *testing.T) {
	ctx := context.Background()

	t.Run("disabling", func(t *testing.T) {
		var gotKey string
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != "/v1/instances/vm_1/address-rotation" {
				t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			}
			gotKey = r.Header.Get("Idempotency-Key")
			_, _ = w.Write([]byte(`{"instance_id":"vm_1","schedule":"daily","connections":"keep","on_demand":false,"state":"disabling","surcharge_percent":50,"on_demand_used":0,"on_demand_limit":10,"events":[]}`))
		})
		got, err := c.DeleteDynamicIP(ctx, "vm_1")
		if err != nil {
			t.Fatalf("DeleteDynamicIP: %v", err)
		}
		if gotKey == "" || got.State != DynamicIPStateDisabling {
			t.Fatalf("key = %q, got = %+v", gotKey, got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"code":"ADDRESS_ROTATION_NOT_FOUND","request_id":"req_3"}`))
		})
		_, err := c.DeleteDynamicIP(ctx, "vm_1")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestRotateDynamicIP(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		var gotKey string
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/instances/vm_1/address-rotation/rotate" {
				t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			}
			gotKey = r.Header.Get("Idempotency-Key")
			_, _ = w.Write([]byte(dynamicIPSampleJSON))
		})
		got, err := c.RotateDynamicIP(ctx, "vm_1")
		if err != nil {
			t.Fatalf("RotateDynamicIP: %v", err)
		}
		if gotKey == "" || got.InstanceID != "vm_1" {
			t.Fatalf("key = %q, got = %+v", gotKey, got)
		}
	})

	t.Run("does not retry gateway errors", func(t *testing.T) {
		calls := 0
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.WriteHeader(http.StatusBadGateway)
		})
		if _, err := c.RotateDynamicIP(ctx, "vm_1"); err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1 (RotateDynamicIP is not replay-safe)", calls)
		}
	})

	t.Run("limit reached", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status":429,"code":"ADDRESS_ROTATION_LIMIT_REACHED","detail":"limit reached","request_id":"req_4"}`))
		})
		c.retry.maxAttempts = 1
		_, err := c.RotateDynamicIP(ctx, "vm_1")
		if !HasCode(err, CodeAddressRotationLimitReached) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCreateInstanceWithAddressRotation(t *testing.T) {
	ctx := context.Background()
	var body map[string]any
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"order_id":"ord_1","instance_id":"vm_1","status":"awaiting_payment","amount_minor":2256000,"currency":"NGN"}`))
	})

	_, err := c.CreateInstance(ctx, CreateInstanceRequest{
		Name:            "web",
		PlanSlug:        "individual",
		ImageSlug:       "ubuntu-24-04",
		SecurityGroupID: "sg_1",
		AddressRotation: &CreateInstanceDynamicIP{
			Schedule:    DynamicIPScheduleDaily,
			Connections: DynamicIPConnectionsMove,
		},
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	rot, ok := body["address_rotation"].(map[string]any)
	if !ok || rot["schedule"] != "daily" || rot["connections"] != "move" {
		t.Fatalf("address_rotation = %#v", body["address_rotation"])
	}
}
