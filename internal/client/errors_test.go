package client

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNewAPIError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantCode   string
		wantReqID  string
		wantSubstr string
	}{
		{"problem json", 404, notFoundBody, "RESOURCE_NOT_FOUND", "req_1", "request_id: req_1"},
		{"not json", 502, "<html>bad gateway</html>", "", "", "Bad Gateway"},
		{"empty body", 500, "", "", "", "Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newAPIError(tt.status, []byte(tt.body))
			if err.Status != tt.status || err.Code != tt.wantCode || err.RequestID != tt.wantReqID {
				t.Fatalf("got %+v", err)
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("Error() = %q, want it to contain %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestNewAPIErrorValidationFields(t *testing.T) {
	body := `{"status":422,"code":"VALIDATION_FAILED","detail":"One or more fields are invalid.","request_id":"req_2","errors":[{"field":"public_key","code":"INVALID_SSH_PUBLIC_KEY","message":"must be a valid OpenSSH public key"}]}`

	err := newAPIError(422, []byte(body))

	if len(err.Errors) != 1 || err.Errors[0].Field != "public_key" || err.Errors[0].Code != "INVALID_SSH_PUBLIC_KEY" {
		t.Fatalf("Errors = %+v", err.Errors)
	}
	for _, want := range []string{"public_key", "must be a valid OpenSSH public key", "req_2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestAPIErrorIs(t *testing.T) {
	tests := []struct {
		name         string
		err          *APIError
		wantNotFound bool
		wantNoRoute  bool
	}{
		{"resource not found", &APIError{Status: 404, Code: "RESOURCE_NOT_FOUND"}, true, false},
		{"gateway no route", &APIError{Status: 404, Code: "GATEWAY_NO_ROUTE"}, false, true},
		{"bare 404 is neither", &APIError{Status: 404}, false, false},
		{"unauthenticated", &APIError{Status: 401, Code: "UNAUTHENTICATED"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, ErrNotFound); got != tt.wantNotFound {
				t.Errorf("Is(ErrNotFound) = %v, want %v", got, tt.wantNotFound)
			}
			if got := errors.Is(tt.err, ErrNoRoute); got != tt.wantNoRoute {
				t.Errorf("Is(ErrNoRoute) = %v, want %v", got, tt.wantNoRoute)
			}
		})
	}
}

func TestHasFieldCode(t *testing.T) {
	err := &APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []FieldError{
		{Field: "plan_slug", Code: "PLAN_NOT_BIGGER"}, {Field: "name", Code: "REQUIRED"},
	}}
	tests := []struct {
		name        string
		err         error
		field, code string
		want        bool
	}{
		{"matches", err, "plan_slug", "PLAN_NOT_BIGGER", true},
		{"matches a second entry", err, "name", "REQUIRED", true},
		{"right field, wrong code", err, "plan_slug", "PLAN_NOT_FOUND", false},
		{"right code, wrong field", err, "name", "PLAN_NOT_BIGGER", false},
		{"no field errors", &APIError{Status: 409, Code: "X"}, "plan_slug", "PLAN_NOT_BIGGER", false},
		{"not an api error", errors.New("boom"), "plan_slug", "PLAN_NOT_BIGGER", false},
		{"wrapped is found", fmt.Errorf("resizing: %w", err), "plan_slug", "PLAN_NOT_BIGGER", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasFieldCode(tt.err, tt.field, tt.code); got != tt.want {
				t.Fatalf("HasFieldCode = %v, want %v", got, tt.want)
			}
		})
	}
}
