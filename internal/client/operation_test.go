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

func TestWaitUntil(t *testing.T) {
	t.Run("polls until the check passes", func(t *testing.T) {
		c, waits := newTestClient(t, func(http.ResponseWriter, *http.Request) {})
		calls := 0
		err := c.WaitUntil(context.Background(), "thing", func(context.Context) (bool, error) { calls++; return calls == 3, nil })
		if err != nil || calls != 3 || len(*waits) != 2 {
			t.Fatalf("err = %v, calls = %d, waits = %v", err, calls, *waits)
		}
	})
	t.Run("a check error ends the wait", func(t *testing.T) {
		c, _ := newTestClient(t, func(http.ResponseWriter, *http.Request) {})
		boom := errors.New("entered failed state")
		if err := c.WaitUntil(context.Background(), "thing", func(context.Context) (bool, error) { return false, boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("stops when the context ends and names what it waited for", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		c, _ := newTestClient(t, func(http.ResponseWriter, *http.Request) {})
		c.retry.sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
		err := c.WaitUntil(ctx, "instance vm_1 to run", func(context.Context) (bool, error) { return false, nil })
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "instance vm_1 to run") {
			t.Fatalf("err = %v", err)
		}
	})
}

// A done check that reports the operation's own failure must not be wrapped in
// "checking whether ... reached its goal", which would hide the real cause.
func TestWaitForOperationPassesAnOperationErrorFromDoneThrough(t *testing.T) {
	c, _ := newTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("the operation should not be fetched") })
	want := &OperationError{Operation: Operation{ID: "op_1", Kind: "create_network", Failure: &OperationFailure{Code: "IP_ADDRESS_UNAVAILABLE"}}}

	err := c.WaitForOperation(context.Background(), "op_1", func(context.Context) (bool, error) { return false, want })

	var got *OperationError
	if !errors.As(err, &got) || got != want {
		t.Fatalf("err = %v, want the OperationError itself", err)
	}
	if strings.Contains(err.Error(), "reached its goal") {
		t.Errorf("err = %q, must not be wrapped", err)
	}
}

// A poll the API cannot answer for a moment (503 PROVISIONING_UNAVAILABLE,
// after the client's own retries) does not fail the wait: the write was
// already accepted, and an apply failed that way left Terraform without the
// resource it had created. The wait goes on and finishes when the
// operation does.
func TestWaitForOperationRidesOutTransientErrors(t *testing.T) {
	for name, status := range map[string]int{"503": http.StatusServiceUnavailable, "429": http.StatusTooManyRequests, "502": http.StatusBadGateway} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls <= 5 { // more than one poll's worth of client retries
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"status":503,"code":"PROVISIONING_UNAVAILABLE","detail":"Wait a few seconds, then retry.","request_id":"req_1"}`))
					return
				}
				_, _ = w.Write([]byte(opJSON("succeeded", "null")))
			})
			if err := c.WaitForOperation(context.Background(), "op_1", nil); err != nil {
				t.Fatalf("err = %v, want the wait to ride out the %d", err, status)
			}
			if calls != 6 {
				t.Fatalf("calls = %d, want 6", calls)
			}
		})
	}
}

// A transient error still ends the wait when ctx does, and is named.
func TestWaitForOperationReportsTheLastTransientErrorWhenItTimesOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"code":"PROVISIONING_UNAVAILABLE","request_id":"req_1"}`))
	})
	polls := 0
	c.retry.sleep = func(ctx context.Context, _ time.Duration) error {
		polls++
		if polls >= 10 {
			cancel()
		}
		return ctx.Err()
	}
	err := c.WaitForOperation(ctx, "op_1", nil)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "PROVISIONING_UNAVAILABLE") {
		t.Fatalf("err = %v, want context.Canceled naming PROVISIONING_UNAVAILABLE", err)
	}
}

// A permanent error still ends the wait at once.
func TestWaitForOperationStopsOnPermanentErrors(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":403,"code":"FORBIDDEN","request_id":"req_1"}`))
	})
	if err := c.WaitForOperation(context.Background(), "op_1", nil); !HasCode(err, "FORBIDDEN") || calls != 1 {
		t.Fatalf("err = %v after %d calls, want FORBIDDEN after 1", err, calls)
	}
}

// A wait asked to slow down waits as long as the server says before its
// next check (Retry-After), not just the poll interval.
func TestWaitForOperationHonorsRetryAfter(t *testing.T) {
	calls := 0
	c, waits := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls <= 3 { // one check's worth of client retries
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":503,"code":"PROVISIONING_UNAVAILABLE","request_id":"req_1"}`))
			return
		}
		_, _ = w.Write([]byte(opJSON("succeeded", "null")))
	})
	if err := c.WaitForOperation(context.Background(), "op_1", nil); err != nil {
		t.Fatal(err)
	}
	if got := *waits; len(got) != 3 || got[2] != 20*time.Second {
		t.Fatalf("waits = %v, want the poll to wait the 20s Retry-After", got)
	}
}

// A Retry-After longer than the cap waits the cap, and a check that
// answers goes back to the poll interval.
func TestPollDelay(t *testing.T) {
	slow := &APIError{Status: http.StatusServiceUnavailable, RetryAfter: 10 * time.Minute}
	if got := pollDelay(2*time.Second, slow); got != maxRetryAfter {
		t.Fatalf("capped = %v", got)
	}
	if got := pollDelay(2*time.Second, nil); got != 2*time.Second {
		t.Fatalf("no error = %v", got)
	}
	if got := pollDelay(5*time.Second, &APIError{RetryAfter: time.Second}); got != 5*time.Second {
		t.Fatalf("shorter Retry-After = %v", got)
	}
}
