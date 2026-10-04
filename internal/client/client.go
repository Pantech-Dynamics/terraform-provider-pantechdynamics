// Package client is a typed HTTP client for the Pantech Dynamics public API.
// It knows nothing about Terraform: it takes and returns plain Go structs.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultTimeout is generous because one backend call has taken ~15s.
	defaultTimeout = 60 * time.Second

	// maxResponseBytes bounds how much of a response body is read.
	maxResponseBytes = 4 << 20
)

// Client talks to the Pantech Dynamics API. Create it with New.
type Client struct {
	baseURL   string
	apiKey    string
	userAgent string
	http      *http.Client
	retry     retryPolicy

	// pollInterval is the pause between operation status checks.
	pollInterval time.Duration
}

// Option customises a Client. Options keep New's signature stable as settings
// are added.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client, for example in tests.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithTimeout sets the per-attempt request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

// WithMaxAttempts sets the total tries per request, including the first.
func WithMaxAttempts(n int) Option {
	return func(c *Client) { c.retry.maxAttempts = max(n, 1) }
}

// New validates the base URL and returns a Client. version is the provider
// version and goes into the User-Agent.
func New(baseURL, apiKey, version string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q: expected an http(s) URL such as https://api.example.com/v1", baseURL)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API key must not be empty")
	}

	c := &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiKey:    apiKey,
		userAgent: "terraform-provider-pantechdynamics/" + version,
		http:      &http.Client{Timeout: defaultTimeout},
		retry:     defaultRetryPolicy(),

		pollInterval: defaultPollInterval,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// requestSpec describes one logical request. It is built once and re-sent as is
// on each retry, so every attempt carries the same Idempotency-Key.
type requestSpec struct {
	method         string
	path           string
	body           []byte
	idempotencyKey string

	// replaySafe marks a mutating call whose endpoint returns the original
	// result when re-sent with the same Idempotency-Key, which makes retrying
	// it on a 5xx or transport error safe. It defaults to false.
	replaySafe bool
}

// callOption customises a single API call.
type callOption func(*requestSpec)

// replaySafe opts a mutating call in to retries on 5xx and transport errors.
// Use it only for endpoints verified to replay on the same Idempotency-Key.
func replaySafe() callOption {
	return func(s *requestSpec) { s.replaySafe = true }
}

// response is a fully read HTTP response. Reading it eagerly means the body is
// always closed before the retry loop decides what to do.
type response struct {
	status int
	header http.Header
	body   []byte
}

// do runs one logical API call: encode the input, send with retries, map a
// failure to a typed error, decode the output. in and out may be nil.
func (c *Client) do(ctx context.Context, method, path string, in, out any, opts ...callOption) error {
	spec, err := newRequestSpec(method, path, in)
	if err != nil {
		return err
	}
	for _, opt := range opts {
		opt(&spec)
	}

	res, err := c.doWithRetry(ctx, spec)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if res.status < 200 || res.status > 299 {
		return newAPIError(res.status, res.body)
	}
	return decodeBody(res.body, out)
}

// newRequestSpec encodes the body and, for methods that change state, mints the
// Idempotency-Key the backend requires. The key is created here, once, outside
// the retry loop.
func newRequestSpec(method, path string, in any) (requestSpec, error) {
	spec := requestSpec{method: method, path: path}

	if in != nil {
		body, err := json.Marshal(in)
		if err != nil {
			return requestSpec{}, fmt.Errorf("encoding request body: %w", err)
		}
		spec.body = body
	}

	if isMutating(method) {
		key, err := newIdempotencyKey()
		if err != nil {
			return requestSpec{}, err
		}
		spec.idempotencyKey = key
	}
	return spec, nil
}

func isMutating(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete
}

// send performs a single HTTP attempt and reads the whole body.
func (c *Client) send(ctx context.Context, spec requestSpec) (response, error) {
	req, err := c.buildRequest(ctx, spec)
	if err != nil {
		return response{}, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return response{}, fmt.Errorf("reading response body: %w", err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// buildRequest creates a fresh *http.Request per attempt, because a request
// body can only be read once.
func (c *Client) buildRequest(ctx context.Context, spec requestSpec) (*http.Request, error) {
	var body io.Reader
	if spec.body != nil {
		body = bytes.NewReader(spec.body)
	}

	req, err := http.NewRequestWithContext(ctx, spec.method, c.baseURL+spec.path, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if spec.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if spec.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", spec.idempotencyKey)
	}
	return req, nil
}

// decodeBody unmarshals a success body into out. An empty body (204) or a nil
// out is fine.
func decodeBody(body []byte, out any) error {
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
