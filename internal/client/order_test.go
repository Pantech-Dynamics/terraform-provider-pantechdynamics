package client

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func orderJSON(status, failureCode, operationID string) string {
	fc, op := "null", "null"
	if failureCode != "" {
		fc = `"` + failureCode + `"`
	}
	if operationID != "" {
		op = `"` + operationID + `"`
	}
	return `{"id":"ord_1","instance_id":"vm_1","status":"` + status + `","operation_id":` + op + `,"failure_code":` + fc + `,"amount_minor":1504000,"currency":"NGN","created_at":"2026-10-03T23:38:51.224304Z","updated_at":"2026-10-03T23:39:26.910076Z"}`
}

func TestGetInstanceOrder(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/instance-orders/ord_1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(orderJSON("provisioned", "", "op_9")))
	})

	order, err := c.GetInstanceOrder(context.Background(), "ord_1")
	if err != nil {
		t.Fatal(err)
	}
	// Reading an order names the field id, where the create response names it order_id.
	if order.ID != "ord_1" || order.InstanceID != "vm_1" || order.OperationID == nil || *order.OperationID != "op_9" || order.AmountMinor != 1504000 || order.CreatedAt == nil {
		t.Fatalf("order = %+v", order)
	}
}

func TestWaitForInstanceOrder(t *testing.T) {
	tests := []struct {
		name      string
		statuses  []string
		failure   string
		wantPolls int
		wantErr   string // substring, "" for success
	}{
		{"already provisioned", []string{"provisioned"}, "", 1, ""},
		{"awaiting payment then provisioned", []string{"awaiting_payment", "paid", "provisioning", "provisioned"}, "", 4, ""},
		{"provisioning fails", []string{"awaiting_payment", "failed"}, "provisioning_handoff_failed", 2, "provisioning_handoff_failed"},
		{"payment is declined", []string{"awaiting_payment", "payment_failed"}, "card_declined", 2, "payment was declined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			polls := 0
			c, waits := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				status := tt.statuses[min(polls, len(tt.statuses)-1)]
				polls++
				op := ""
				if status == "provisioned" {
					op = "op_9"
				}
				failure := ""
				if status == "failed" || status == "payment_failed" {
					failure = tt.failure
				}
				_, _ = w.Write([]byte(orderJSON(status, failure, op)))
			})

			order, err := c.WaitForInstanceOrder(context.Background(), "ord_1")

			if polls != tt.wantPolls {
				t.Fatalf("polls = %d, want %d", polls, tt.wantPolls)
			}
			if want := slices.Repeat([]time.Duration{defaultPollInterval}, tt.wantPolls-1); !slices.Equal(*waits, want) {
				t.Fatalf("waits = %v, want %v", *waits, want)
			}
			if tt.wantErr == "" {
				if err != nil || order == nil || order.OperationID == nil || *order.OperationID != "op_9" {
					t.Fatalf("order = %+v, err = %v", order, err)
				}
				return
			}
			var orderErr *OrderError
			if !errors.As(err, &orderErr) || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "ord_1") || order != nil {
				t.Fatalf("err = %v, want an OrderError mentioning %q and the order id", err, tt.wantErr)
			}
		})
	}
}

func TestWaitForInstanceOrderStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(orderJSON("awaiting_payment", "", "")))
	})
	c.retry.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	_, err := c.WaitForInstanceOrder(ctx, "ord_1")

	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "awaiting_payment") {
		t.Fatalf("err = %v, want context.Canceled mentioning the last status", err)
	}
}

func TestWaitForInstanceOrderLookupFailure(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(notFoundBody))
	})
	if _, err := c.WaitForInstanceOrder(context.Background(), "ord_x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
