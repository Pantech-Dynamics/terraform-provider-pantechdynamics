package webhook

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Limits the backend enforces on a webhook (CreateWebhookRequest in the public
// contract). Checking them here turns a 422 after apply into a plan-time error.
const (
	maxURLLength         = 2048
	maxDescriptionLength = 255
	maxEvents            = 100
)

// httpsURL checks that the URL is https and not too long. Whether the host is on
// the public internet is left to the backend, which resolves it.
type httpsURL struct{}

func (httpsURL) Description(context.Context) string {
	return fmt.Sprintf("must be an https URL of at most %d characters", maxURLLength)
}

func (v httpsURL) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (httpsURL) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if !strings.HasPrefix(strings.ToLower(s), "https://") || len(s) <= len("https://") {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid webhook URL", fmt.Sprintf("Webhooks are sent only to https URLs, got %q.", s))
		return
	}
	if len(s) > maxURLLength {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid webhook URL", fmt.Sprintf("The URL is %d characters long; at most %d are allowed.", len(s), maxURLLength))
	}
}

// maxLength checks a string's length in characters.
type maxLength struct{ max int }

func (v maxLength) Description(context.Context) string {
	return fmt.Sprintf("must be at most %d characters", v.max)
}

func (v maxLength) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v maxLength) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := utf8.RuneCountInString(req.ConfigValue.ValueString()); n > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Value too long", fmt.Sprintf("Got %d characters; at most %d are allowed.", n, v.max))
	}
}

// eventList checks the event subscriptions: 1 to 100 entries, none empty, none
// repeated. The backend drops a repeat, which would make the applied list differ
// from the configuration, so a repeat is refused here instead.
type eventList struct{}

func (eventList) Description(context.Context) string {
	return fmt.Sprintf("must hold 1 to %d distinct, non-empty event types", maxEvents)
}

func (v eventList) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (eventList) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) == 0 || len(elems) > maxEvents {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid events", fmt.Sprintf("Subscribe to 1 to %d events, got %d.", maxEvents, len(elems)))
		return
	}

	seen := map[string]bool{}
	for _, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() || s.IsNull() {
			continue
		}
		val := s.ValueString()
		if strings.TrimSpace(val) == "" {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid events", "An event type must not be empty. Use an event type, \"<resource>.*\" or \"*\".")
			return
		}
		if seen[val] {
			resp.Diagnostics.AddAttributeError(req.Path, "Duplicate event", fmt.Sprintf("%q is listed more than once. List each event once.", val))
			return
		}
		seen[val] = true
	}
}
