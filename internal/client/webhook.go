package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the webhook endpoints return.
const (
	// CodeWebhookNotFound is the 404 for a webhook id that is not in the
	// organization, including one already deleted. It counts as ErrNotFound.
	CodeWebhookNotFound = "WEBHOOK_NOT_FOUND"

	// CodeWebhookLimitReached is the 403 when the organization already has the
	// most webhooks it may have (10).
	CodeWebhookLimitReached = "WEBHOOK_LIMIT_REACHED"

	// CodeWebhooksUnavailable is the 503 when webhooks are not offered on this
	// deployment yet.
	CodeWebhooksUnavailable = "WEBHOOKS_UNAVAILABLE"
)

// Webhook is a URL the platform sends the organization's events to. The signing
// secret is never part of it, except in the create response (see
// WebhookWithSecret).
type Webhook struct {
	ID             string     `json:"id"`
	URL            string     `json:"url"`
	Description    string     `json:"description"`
	Events         []string   `json:"events"`
	Enabled        bool       `json:"enabled"`
	DisabledReason *string    `json:"disabled_reason"`
	FailingSince   *time.Time `json:"failing_since"`
	CreatedAt      *time.Time `json:"created_at"`
	UpdatedAt      *time.Time `json:"updated_at"`
}

// WebhookWithSecret is the create response: the webhook plus its signing
// secret (whsec_...). Secret is nil when the create was replayed on the same
// Idempotency-Key, because the backend shows a secret only once.
type WebhookWithSecret struct {
	Webhook
	Secret *string `json:"secret"`
}

// CreateWebhookRequest adds a webhook. Events keeps the caller's order.
type CreateWebhookRequest struct {
	URL         string   `json:"url"`
	Description string   `json:"description,omitempty"`
	Events      []string `json:"events"`
}

// UpdateWebhookRequest changes a webhook. A nil field is left out of the body,
// and the backend keeps its value. Pointers are needed so false and "" can
// still be sent.
type UpdateWebhookRequest struct {
	URL         *string   `json:"url,omitempty"`
	Description *string   `json:"description,omitempty"`
	Events      *[]string `json:"events,omitempty"`
	Enabled     *bool     `json:"enabled,omitempty"`
}

// WebhookEventType is one event a webhook can subscribe to.
type WebhookEventType struct {
	Type        string `json:"type"`
	Resource    string `json:"resource"`
	Description string `json:"description"`
}

// CreateWebhook adds a webhook. It is retried on gateway errors because the
// backend replays a create on the same Idempotency-Key. The replay carries
// Secret nil, which callers must handle: the webhook exists but its secret was
// shown in the lost response only.
func (c *Client) CreateWebhook(ctx context.Context, req CreateWebhookRequest) (*WebhookWithSecret, error) {
	var w WebhookWithSecret
	if err := c.do(ctx, http.MethodPost, "/webhooks", req, &w, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating webhook: %w", err)
	}
	return &w, nil
}

// GetWebhook returns one webhook. It returns ErrNotFound when it does not exist.
func (c *Client) GetWebhook(ctx context.Context, id string) (*Webhook, error) {
	var w Webhook
	if err := c.do(ctx, http.MethodGet, webhookPath(id), nil, &w); err != nil {
		return nil, fmt.Errorf("getting webhook %s: %w", id, err)
	}
	return &w, nil
}

// ListWebhooks returns every webhook of the organization.
func (c *Client) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	hooks, err := listAll[Webhook](ctx, c, "/webhooks")
	if err != nil {
		return nil, fmt.Errorf("listing webhooks: %w", err)
	}
	return hooks, nil
}

// UpdateWebhook changes the fields set in req. The same change applied twice
// leaves the webhook the same, so it is retried on gateway errors.
func (c *Client) UpdateWebhook(ctx context.Context, id string, req UpdateWebhookRequest) (*Webhook, error) {
	var w Webhook
	if err := c.do(ctx, http.MethodPatch, webhookPath(id), req, &w, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating webhook %s: %w", id, err)
	}
	return &w, nil
}

// DeleteWebhook removes a webhook and its delivery history. It returns
// ErrNotFound when the webhook is already gone, which callers treat as success.
// The backend does not replay a delete, so it is not retried on a 5xx or
// transport error; a repeat after a lost response would answer 404 anyway.
func (c *Client) DeleteWebhook(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, webhookPath(id), nil, nil); err != nil {
		return fmt.Errorf("deleting webhook %s: %w", id, err)
	}
	return nil
}

// ListWebhookEventTypes returns every event type a webhook can subscribe to.
func (c *Client) ListWebhookEventTypes(ctx context.Context) ([]WebhookEventType, error) {
	types, err := listAll[WebhookEventType](ctx, c, "/webhook-event-types")
	if err != nil {
		return nil, fmt.Errorf("listing webhook event types: %w", err)
	}
	return types, nil
}

// webhookPath escapes the id so a malformed value cannot alter the URL path.
func webhookPath(id string) string {
	return "/webhooks/" + url.PathEscape(id)
}
