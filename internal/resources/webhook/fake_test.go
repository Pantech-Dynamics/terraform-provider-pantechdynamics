package webhook

import (
	"context"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// fakeAPI is an in-memory webhookAPI. A non-nil *Err is returned by the matching
// method. With noSecret set, a create answers as a replay does: no secret.
type fakeAPI struct {
	hooks map[string]client.Webhook

	createErr, getErr, updateErr, deleteErr error
	noSecret                                bool

	nextID     int
	creates    int
	updates    int
	deletes    int
	lastCreate client.CreateWebhookRequest
	lastUpdate client.UpdateWebhookRequest
}

func newFake() *fakeAPI { return &fakeAPI{hooks: map[string]client.Webhook{}} }

func at(sec int) *time.Time {
	t := time.Date(2026, 10, 22, 10, 0, sec, 123456000, time.UTC)
	return &t
}

func (f *fakeAPI) CreateWebhook(_ context.Context, req client.CreateWebhookRequest) (*client.WebhookWithSecret, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	h := client.Webhook{
		ID: fmt.Sprintf("whk_%d", f.nextID), URL: req.URL, Description: req.Description,
		Events: append([]string(nil), req.Events...), Enabled: true, CreatedAt: at(0), UpdatedAt: at(0),
	}
	f.hooks[h.ID] = h
	out := &client.WebhookWithSecret{Webhook: h}
	if !f.noSecret {
		s := "whsec_first"
		out.Secret = &s
	}
	return out, nil
}

func (f *fakeAPI) GetWebhook(_ context.Context, id string) (*client.Webhook, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	h, ok := f.hooks[id]
	if !ok {
		return nil, fmt.Errorf("getting webhook: %w", &client.APIError{Status: 404, Code: client.CodeWebhookNotFound})
	}
	return &h, nil
}

func (f *fakeAPI) UpdateWebhook(_ context.Context, id string, req client.UpdateWebhookRequest) (*client.Webhook, error) {
	f.updates++
	f.lastUpdate = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	h, ok := f.hooks[id]
	if !ok {
		return nil, fmt.Errorf("updating webhook: %w", &client.APIError{Status: 404, Code: client.CodeWebhookNotFound})
	}
	if req.URL != nil {
		h.URL = *req.URL
	}
	if req.Description != nil {
		h.Description = *req.Description
	}
	if req.Events != nil {
		h.Events = *req.Events
	}
	if req.Enabled != nil {
		h.Enabled = *req.Enabled
		h.DisabledReason = nil
		h.FailingSince = nil
		if !h.Enabled {
			r := "disabled_by_user"
			h.DisabledReason = &r
		}
	}
	h.UpdatedAt = at(30)
	f.hooks[id] = h
	return &h, nil
}

func (f *fakeAPI) DeleteWebhook(_ context.Context, id string) error {
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.hooks[id]; !ok {
		return fmt.Errorf("deleting webhook: %w", &client.APIError{Status: 404, Code: client.CodeWebhookNotFound})
	}
	delete(f.hooks, id)
	return nil
}
