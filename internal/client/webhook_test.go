package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const webhookJSON = `{"id":"whk_1","url":"https://example.com/hooks","description":"","events":["instance.*","invoice.paid"],"enabled":true,"disabled_reason":null,"failing_since":null,"created_at":"2026-10-22T10:00:00.123456Z","updated_at":"2026-10-22T10:00:00.123456Z"}`

func TestCreateWebhook(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantSecret string
		wantErr    func(error) bool
	}{
		{"created with its secret", 201, strings.Replace(webhookJSON, "}", `,"secret":"whsec_abc"}`, 1), "whsec_abc", nil},
		{"replay has no secret", 201, strings.Replace(webhookJSON, "}", `,"secret":null}`, 1), "", nil},
		{"limit reached", 403, `{"status":403,"code":"WEBHOOK_LIMIT_REACHED"}`, "", func(e error) bool { return HasCode(e, CodeWebhookLimitReached) }},
		{"invalid url", 422, `{"status":422,"code":"VALIDATION_FAILED","errors":[{"field":"url","code":"INVALID_WEBHOOK_URL","message":"bad"}]}`, "", func(e error) bool { return HasFieldCode(e, "url", "INVALID_WEBHOOK_URL") }},
		{"malformed json", 201, `{"id":`, "", func(e error) bool { return e != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/webhooks" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Idempotency-Key") == "" {
					t.Error("missing Idempotency-Key")
				}
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"events":["instance.*","invoice.paid"]`) || strings.Contains(string(body), "description") {
					t.Errorf("body = %s, want events in order and no empty description", body)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			hook, err := c.CreateWebhook(context.Background(), CreateWebhookRequest{URL: "https://example.com/hooks", Events: []string{"instance.*", "invoice.paid"}})
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if hook.ID != "whk_1" || len(hook.Events) != 2 || hook.CreatedAt == nil || derefString(hook.Secret) != tt.wantSecret {
				t.Fatalf("hook = %+v", hook)
			}
		})
	}
}

// A create replays on the same Idempotency-Key, so a gateway error is retried
// with the same key.
func TestCreateWebhookRetriesGatewayErrorsWithTheSameKey(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(strings.Replace(webhookJSON, "}", `,"secret":null}`, 1)))
	})
	if _, err := c.CreateWebhook(context.Background(), CreateWebhookRequest{URL: "https://example.com/hooks", Events: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys = %v, want two tries with one key", keys)
	}
}

func TestGetWebhook(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantNotFound bool
	}{
		{"found", 200, webhookJSON, false},
		{"deleted", 404, `{"status":404,"code":"WEBHOOK_NOT_FOUND","request_id":"req_1"}`, true},
		{"not a webhook id", 404, notFoundBody, true},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != "/v1/webhooks/whk_1" {
					t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			hook, err := c.GetWebhook(context.Background(), "whk_1")
			if got := errors.Is(err, ErrNotFound); got != tt.wantNotFound {
				t.Fatalf("errors.Is(ErrNotFound) = %v, err = %v", got, err)
			}
			if tt.status == 200 && (err != nil || hook.URL != "https://example.com/hooks" || hook.DisabledReason != nil) {
				t.Fatalf("hook = %+v, err = %v", hook, err)
			}
			if tt.status == 401 && err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestUpdateWebhookSendsOnlySetFields(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/webhooks/whk_1" || r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"description":"","enabled":false}` {
			t.Errorf("body = %s", body)
		}
		_, _ = w.Write([]byte(strings.Replace(webhookJSON, `"enabled":true,"disabled_reason":null`, `"enabled":false,"disabled_reason":"disabled_by_user"`, 1)))
	})
	empty, off := "", false
	hook, err := c.UpdateWebhook(context.Background(), "whk_1", UpdateWebhookRequest{Description: &empty, Enabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if hook.Enabled || derefString(hook.DisabledReason) != "disabled_by_user" {
		t.Fatalf("hook = %+v", hook)
	}
}

func TestDeleteWebhook(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantErr      bool
		wantNotFound bool
		wantCalls    int
	}{
		{"deleted", 204, "", false, false, 1},
		{"already gone", 404, `{"status":404,"code":"WEBHOOK_NOT_FOUND"}`, true, true, 1},
		{"not retried on 502", 502, "", true, false, 1},
		{"rate limited is retried", 429, `{"status":429,"code":"RATE_LIMITED"}`, true, false, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/webhooks/whk_1" || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			err := c.DeleteWebhook(context.Background(), "whk_1")
			if (err != nil) != tt.wantErr || errors.Is(err, ErrNotFound) != tt.wantNotFound {
				t.Fatalf("err = %v", err)
			}
			if calls != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestListWebhookEventTypes(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/webhook-event-types" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"data":[{"type":"instance.created","resource":"instance","description":"An instance finished creating."}],"next_cursor":"c2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"type":"invoice.paid","resource":"invoice","description":"An invoice was paid."}],"next_cursor":null}`))
	})
	types, err := c.ListWebhookEventTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 || types[0].Type != "instance.created" || types[1].Resource != "invoice" {
		t.Fatalf("types = %+v", types)
	}
}

func TestListWebhookEventTypesUnavailable(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"status":503,"code":"WEBHOOKS_UNAVAILABLE"}`))
	})
	if _, err := c.ListWebhookEventTypes(context.Background()); !HasCode(err, CodeWebhooksUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestListWebhooks(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[` + webhookJSON + `],"next_cursor":null}`))
	})
	hooks, err := c.ListWebhooks(context.Background())
	if err != nil || len(hooks) != 1 || hooks[0].ID != "whk_1" {
		t.Fatalf("hooks = %+v, err = %v", hooks, err)
	}
}
