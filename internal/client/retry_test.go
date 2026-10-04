package client

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"
)

func TestShouldRetry(t *testing.T) {
	p := defaultRetryPolicy()
	netErr := errors.New("connection reset")
	tests := []struct {
		name       string
		method     string
		replaySafe bool
		status     int
		err        error
		want       bool
	}{
		{"GET 500", http.MethodGet, false, 500, nil, true},
		{"GET 429", http.MethodGet, false, 429, nil, true},
		{"GET network error", http.MethodGet, false, 0, netErr, true},
		{"GET 404", http.MethodGet, false, 404, nil, false},
		{"GET 200", http.MethodGet, false, 200, nil, false},
		{"POST 429 always retried", http.MethodPost, false, 429, nil, true},
		{"DELETE 429 always retried", http.MethodDelete, false, 429, nil, true},
		{"POST 503 by default not retried", http.MethodPost, false, 503, nil, false},
		{"DELETE 502 by default not retried", http.MethodDelete, false, 502, nil, false},
		{"POST network error by default not retried", http.MethodPost, false, 0, netErr, false},
		{"POST 503 replaySafe retried", http.MethodPost, true, 503, nil, true},
		{"POST 502 replaySafe retried", http.MethodPost, true, 502, nil, true},
		{"POST 504 replaySafe retried", http.MethodPost, true, 504, nil, true},
		{"POST 500 replaySafe not retried", http.MethodPost, true, 500, nil, false},
		{"DELETE 500 replaySafe not retried", http.MethodDelete, true, 500, nil, false},
		{"POST network error replaySafe retried", http.MethodPost, true, 0, netErr, true},
		{"POST 400 replaySafe not retried", http.MethodPost, true, 400, nil, false},
		{"POST 409 replaySafe not retried", http.MethodPost, true, 409, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := requestSpec{method: tt.method, replaySafe: tt.replaySafe}
			if got := p.shouldRetry(spec, tt.status, tt.err); got != tt.want {
				t.Fatalf("shouldRetry = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDelay(t *testing.T) {
	p := defaultRetryPolicy()
	p.jitter = func(d time.Duration) time.Duration { return d }

	tests := []struct {
		name       string
		attempt    int
		retryAfter time.Duration
		want       time.Duration
	}{
		{"first", 1, 0, 500 * time.Millisecond},
		{"second doubles", 2, 0, time.Second},
		{"capped", 10, 0, defaultMaxDelay},
		{"retry-after wins", 1, 3 * time.Second, 3 * time.Second},
		{"retry-after capped", 1, time.Hour, maxRetryAfter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.delay(tt.attempt, tt.retryAfter); got != tt.want {
				t.Fatalf("delay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := map[string]time.Duration{"5": 5 * time.Second, "": 0, "abc": 0, "-1": 0, "0": 0}
	for in, want := range tests {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestHalfJitterRange(t *testing.T) {
	d := 8 * time.Second
	for range 200 {
		if got := halfJitter(d); got < d/2 || got > d {
			t.Fatalf("halfJitter = %v, want within [%v, %v]", got, d/2, d)
		}
	}
}

func TestRetryThenSuccess(t *testing.T) {
	calls := 0
	c, waits := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"id":"x_1"}`))
	})

	var out item
	if err := c.do(context.Background(), http.MethodGet, "/x", nil, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || out.ID != "x_1" {
		t.Fatalf("calls = %d, out = %+v", calls, out)
	}
	if want := []time.Duration{500 * time.Millisecond, time.Second}; !slices.Equal(*waits, want) {
		t.Fatalf("waits = %v, want %v", *waits, want)
	}
}

func TestRetryGivesUp(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":500,"code":"INTERNAL","request_id":"req_9"}`))
	})

	err := c.do(context.Background(), http.MethodGet, "/x", nil, nil)

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.RequestID != "req_9" {
		t.Fatalf("err = %v, want the final 500 APIError", err)
	}
	if calls != defaultMaxAttempts {
		t.Fatalf("calls = %d, want %d", calls, defaultMaxAttempts)
	}
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	calls := 0
	c, waits := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.do(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{2 * time.Second}; !slices.Equal(*waits, want) {
		t.Fatalf("waits = %v, want %v", *waits, want)
	}
}

func TestMutatingCallNotRetriedOn5xxByDefault(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	})

	err := c.do(context.Background(), http.MethodPost, "/x", item{Name: "a"}, nil)

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 502 {
		t.Fatalf("err = %v, want the 502 APIError", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1: a POST must not be blindly retried", calls)
	}
}

func TestMutatingCallRetriedOn429(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.do(context.Background(), http.MethodPost, "/x", item{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("keys = %v, want the same key on the retry after a 429", keys)
	}
}

func TestIdempotencyKeyAcrossRetries(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	ctx := context.Background()
	if err := c.do(ctx, http.MethodPost, "/x", item{Name: "a"}, nil, replaySafe()); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("keys = %v, want one non-empty key reused on every attempt", keys)
	}

	keys = nil
	if err := c.do(ctx, http.MethodDelete, "/x/1", nil, nil, replaySafe()); err != nil {
		t.Fatal(err)
	}
	if keys[0] == "" {
		t.Fatal("DELETE must send an Idempotency-Key")
	}
}

func TestNewLogicalCallGetsNewKey(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusNoContent)
	})

	for range 2 {
		if err := c.do(context.Background(), http.MethodPost, "/x", item{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if keys[0] == keys[1] {
		t.Fatalf("two logical calls shared key %q", keys[0])
	}
}

func TestGetSendsNoIdempotencyKey(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "" {
			t.Errorf("GET sent Idempotency-Key %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.do(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c.retry.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	err := c.do(ctx, http.MethodGet, "/x", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestSleepContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
