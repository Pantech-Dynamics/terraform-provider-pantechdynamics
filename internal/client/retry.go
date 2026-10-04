package client

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Retry defaults. Three attempts keeps a failing apply from hanging, and the
// delays stay well under the 120 requests/minute rate limit.
const (
	defaultMaxAttempts = 3
	defaultBaseDelay   = 500 * time.Millisecond
	defaultMaxDelay    = 8 * time.Second
	maxRetryAfter      = 30 * time.Second
)

// retryPolicy decides whether and when to retry. sleep and jitter are fields,
// not package functions, so tests can replace them and run without waiting.
type retryPolicy struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
	sleep       func(ctx context.Context, d time.Duration) error
	jitter      func(d time.Duration) time.Duration
}

func defaultRetryPolicy() retryPolicy {
	return retryPolicy{
		maxAttempts: defaultMaxAttempts,
		baseDelay:   defaultBaseDelay,
		maxDelay:    defaultMaxDelay,
		sleep:       sleepContext,
		jitter:      halfJitter,
	}
}

// shouldRetry implements the policy:
//   - 429 is retried for every method: the server rejected the request before
//     acting on it, so re-sending cannot duplicate anything.
//   - GET is also retried on any 5xx and on transport errors, since it changes
//     nothing.
//   - POST, PUT and DELETE are retried on transport errors and gateway errors
//     (502, 503, 504) only when the call is marked replaySafe. A plain 500 is
//     not retried: the backend returns it for deterministic failures such as a
//     duplicate security group name, where retrying only repeats a slow call.
//     Endpoints that do not replay on an Idempotency-Key (ssh keys) never opt in.
func (p retryPolicy) shouldRetry(spec requestSpec, status int, err error) bool {
	if err != nil {
		return spec.method == http.MethodGet || spec.replaySafe
	}
	if status == http.StatusTooManyRequests {
		return true
	}
	if spec.method == http.MethodGet {
		return status >= http.StatusInternalServerError
	}
	return spec.replaySafe && isGatewayError(status)
}

// isGatewayError reports the statuses that mean "the request may not have
// reached the application", as opposed to the application failing.
func isGatewayError(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// delay is exponential backoff with jitter, or the server's Retry-After when
// it sent one.
func (p retryPolicy) delay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, maxRetryAfter)
	}
	d := p.baseDelay << (attempt - 1)
	if d <= 0 || d > p.maxDelay {
		d = p.maxDelay
	}
	return p.jitter(d)
}

// doWithRetry re-sends the same request, with the same Idempotency-Key, until it
// succeeds, fails permanently, runs out of attempts, or ctx is cancelled.
// After the last attempt it returns the final response so the caller can turn
// it into a typed error.
func (c *Client) doWithRetry(ctx context.Context, spec requestSpec) (response, error) {
	for attempt := 1; ; attempt++ {
		res, err := c.send(ctx, spec)
		if attempt >= c.retry.maxAttempts || !c.retry.shouldRetry(spec, res.status, err) {
			return res, err
		}
		wait := c.retry.delay(attempt, parseRetryAfter(res.header.Get("Retry-After")))
		if sleepErr := c.retry.sleep(ctx, wait); sleepErr != nil {
			return response{}, sleepErr
		}
	}
}

// parseRetryAfter reads the delay-in-seconds form of Retry-After. The HTTP-date
// form is ignored and falls back to backoff.
func parseRetryAfter(value string) time.Duration {
	secs, err := strconv.Atoi(value)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// sleepContext waits for d or until ctx is done, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// halfJitter returns a random duration in [d/2, d], spreading out clients that
// were throttled at the same moment.
func halfJitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half+1)
}
