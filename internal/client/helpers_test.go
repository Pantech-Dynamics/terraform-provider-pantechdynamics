package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient points a Client at a test server and removes real waiting. The
// returned slice records every backoff the client asked for.
func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := New(srv.URL+"/v1", "PAN_test", "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var waits []time.Duration
	c.retry.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	c.retry.jitter = func(d time.Duration) time.Duration { return d }
	return c, &waits
}

const notFoundBody = `{"title":"Resource not found","status":404,"code":"RESOURCE_NOT_FOUND","detail":"No resource matches this request.","request_id":"req_1"}`
