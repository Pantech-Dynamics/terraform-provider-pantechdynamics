package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		apiKey  string
		wantErr bool
	}{
		{"ok", "https://api.example.com/v1", "PAN_x", false},
		{"trailing slash ok", "https://api.example.com/v1/", "PAN_x", false},
		{"no scheme", "api.example.com/v1", "PAN_x", true},
		{"wrong scheme", "ftp://api.example.com", "PAN_x", true},
		{"empty url", "", "PAN_x", true},
		{"empty key", "https://api.example.com/v1", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.baseURL, tt.apiKey, "test")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDoSuccessAndHeaders(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer PAN_test" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "terraform-provider-pantechdynamics/test" {
			t.Errorf("User-Agent = %q", got)
		}
		if r.URL.Path != "/v1/things" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"name":"a"`) {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"x_1","name":"a"}`))
	})

	var out item
	if err := c.do(context.Background(), http.MethodPost, "/things", item{Name: "a"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "x_1" {
		t.Fatalf("out = %+v", out)
	}
}

func TestDoErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		check      func(error) bool
	}{
		{"not found", 404, notFoundBody, 404, func(e error) bool { return errors.Is(e, ErrNotFound) }},
		{"no route", 404, `{"status":404,"code":"GATEWAY_NO_ROUTE"}`, 404, func(e error) bool { return errors.Is(e, ErrNoRoute) && !errors.Is(e, ErrNotFound) }},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, 401, func(e error) bool { return !errors.Is(e, ErrNotFound) }},
		{"validation not retried", 422, `{"status":422,"code":"VALIDATION_FAILED"}`, 422, func(e error) bool { return true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.do(context.Background(), http.MethodGet, "/x", nil, nil)

			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tt.wantStatus {
				t.Fatalf("err = %v, want APIError with status %d", err, tt.wantStatus)
			}
			if !tt.check(err) {
				t.Fatalf("unexpected error classification: %v", err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1 (4xx must not be retried)", calls)
			}
		})
	}
}

func TestDoMalformedJSON(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	})
	var out item
	err := c.do(context.Background(), http.MethodGet, "/x", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "decoding response") {
		t.Fatalf("err = %v, want decoding error", err)
	}
}

func TestDoNoContent(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.do(context.Background(), http.MethodDelete, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
}

// WithTimeout must bound each attempt: a server slower than the timeout fails
// the call, and a longer timeout lets the same server succeed.
func TestWithTimeoutBoundsEachRequest(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`))
	}))
	t.Cleanup(slow.Close)

	short, err := New(slow.URL+"/v1", "PAN_test", "test", WithTimeout(50*time.Millisecond), WithMaxAttempts(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := short.ListSSHKeys(context.Background()); err == nil {
		t.Error("a 50ms timeout against a 300ms server should fail")
	}

	long, err := New(slow.URL+"/v1", "PAN_test", "test", WithTimeout(5*time.Second), WithMaxAttempts(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := long.ListSSHKeys(context.Background()); err != nil {
		t.Errorf("a 5s timeout against a 300ms server should succeed: %v", err)
	}
}
