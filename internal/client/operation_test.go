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

func opJSON(status, failure string) string {
	return `{"id":"op_1","resource_type":"security_group","resource_id":"sg_1","kind":"create_security_group","status":"` + status + `","failure":` + failure + `,"created_at":"2026-10-03T22:57:03.518006Z","updated_at":"2026-10-03T22:57:03.911715Z"}`
}

func TestGetOperation(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/operations/op_1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(opJSON("submitted", "null")))
	})

	op, err := c.GetOperation(context.Background(), "op_1")
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != OperationSubmitted || op.Kind != "create_security_group" || op.Failure != nil || op.CreatedAt == nil {
		t.Fatalf("op = %+v", op)
	}
}

func TestGetOperationNotFound(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(notFoundBody))
	})
	if _, err := c.GetOperation(context.Background(), "op_gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestWaitForOperation(t *testing.T) {
	tests := []struct {
		name      string
		statuses  []string // one response per poll, in order
		failure   string
		wantPolls int
		wantErr   bool
	}{
		{"already done", []string{"succeeded"}, "null", 1, false},
		{"pending then done", []string{"submitting", "submitted", "succeeded"}, "null", 3, false},
		{"fails with a code", []string{"submitted", "failed"}, `{"code":"CAPACITY","reason":"no room"}`, 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			polls := 0
			c, waits := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				status := tt.statuses[min(polls, len(tt.statuses)-1)]
				polls++
				failure := "null"
				if status == "failed" {
					failure = tt.failure
				}
				_, _ = w.Write([]byte(opJSON(status, failure)))
			})

			err := c.WaitForOperation(context.Background(), "op_1", nil)

			if polls != tt.wantPolls {
				t.Fatalf("polls = %d, want %d", polls, tt.wantPolls)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr {
				var opErr *OperationError
				if !errors.As(err, &opErr) || opErr.Operation.Failure.Code != "CAPACITY" || !strings.Contains(err.Error(), "CAPACITY") || !strings.Contains(err.Error(), "op_1") {
					t.Fatalf("err = %v, want an OperationError with the code and operation id", err)
				}
			}
			if want := slices.Repeat([]time.Duration{defaultPollInterval}, tt.wantPolls-1); !slices.Equal(*waits, want) {
				t.Fatalf("waits = %v, want %v", *waits, want)
			}
		})
	}
}

func TestWaitForOperationDoneCheck(t *testing.T) {
	t.Run("finishes on the resource even if the operation never completes", func(t *testing.T) {
		opPolls, checks := 0, 0
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			opPolls++
			_, _ = w.Write([]byte(opJSON("submitted", "null"))) // stuck, as seen on dev
		})
		check := func(context.Context) (bool, error) { checks++; return checks >= 3, nil }

		if err := c.WaitForOperation(context.Background(), "op_1", check); err != nil {
			t.Fatal(err)
		}
		if checks != 3 || opPolls != 2 {
			t.Fatalf("checks = %d, opPolls = %d, want the check to end the wait before a third operation poll", checks, opPolls)
		}
	})

	t.Run("a failed operation still fails the wait", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(opJSON("failed", `{"code":"BOOM","reason":"x"}`)))
		})
		err := c.WaitForOperation(context.Background(), "op_1", func(context.Context) (bool, error) { return false, nil })
		var opErr *OperationError
		if !errors.As(err, &opErr) {
			t.Fatalf("err = %v, want OperationError", err)
		}
	})

	t.Run("a check error stops the wait", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(opJSON("submitted", "null")))
		})
		boom := errors.New("lookup failed")
		err := c.WaitForOperation(context.Background(), "op_1", func(context.Context) (bool, error) { return false, boom })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWaitForOperationStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	polls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		polls++
		_, _ = w.Write([]byte(opJSON("submitted", "null")))
	})
	c.retry.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	err := c.WaitForOperation(ctx, "op_1", nil)

	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "submitted") {
		t.Fatalf("err = %v, want context.Canceled mentioning the last status", err)
	}
	if polls != 1 {
		t.Fatalf("polls = %d", polls)
	}
}

func TestWaitForOperationReportsLookupFailure(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":401,"code":"UNAUTHENTICATED"}`))
	})
	if err := c.WaitForOperation(context.Background(), "op_1", nil); !HasCode(err, "UNAUTHENTICATED") {
		t.Fatalf("err = %v", err)
	}
}
