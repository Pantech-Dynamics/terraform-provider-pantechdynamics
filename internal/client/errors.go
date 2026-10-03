package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Sentinel errors let callers branch with errors.Is, much like catching a
// specific exception type in Java.
var (
	// ErrNotFound means the backend answered RESOURCE_NOT_FOUND. Read removes
	// the resource from state on it, and Delete treats it as success.
	ErrNotFound = errors.New("resource not found")

	// ErrNoRoute means the gateway has no route for the request. That is a
	// wrong base_url, not a missing resource, so it must never be confused
	// with ErrNotFound.
	ErrNoRoute = errors.New("no API route at this base URL")
)

// Backend problem codes the client branches on.
const (
	codeResourceNotFound = "RESOURCE_NOT_FOUND"
	codeGatewayNoRoute   = "GATEWAY_NO_ROUTE"
)

// FieldError is one entry of a 422 validation failure.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// APIError is an application/problem+json error returned by the backend.
type APIError struct {
	Status    int
	Code      string
	Title     string
	Detail    string
	RequestID string

	// Errors is set on 422 responses and names each invalid field, so the
	// provider can point at the exact attribute.
	Errors []FieldError
}

// problemDocument mirrors the backend's problem+json body.
type problemDocument struct {
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Code      string       `json:"code"`
	Detail    string       `json:"detail"`
	RequestID string       `json:"request_id"`
	Errors    []FieldError `json:"errors"`
}

// Error includes the request ID so a user can quote it to support.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("API error %d", e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	} else if e.Title != "" {
		msg += ": " + e.Title
	}
	for _, fe := range e.Errors {
		msg += fmt.Sprintf("; %s: %s (%s)", fe.Field, fe.Message, fe.Code)
	}
	if e.RequestID != "" {
		msg += " (request_id: " + e.RequestID + ")"
	}
	return msg
}

// Is maps backend codes to the sentinel errors. It branches on the problem
// code, not only the HTTP status, because a 404 can mean two different things.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Code == codeResourceNotFound
	case ErrNoRoute:
		return e.Code == codeGatewayNoRoute
	default:
		return false
	}
}

// newAPIError builds an APIError from a non-2xx response. A body that is not
// problem+json still yields a usable error built from the status line.
func newAPIError(status int, body []byte) *APIError {
	apiErr := &APIError{Status: status, Title: http.StatusText(status)}

	var doc problemDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return apiErr
	}

	apiErr.Code = doc.Code
	apiErr.Detail = doc.Detail
	apiErr.RequestID = doc.RequestID
	apiErr.Errors = doc.Errors
	if doc.Title != "" {
		apiErr.Title = doc.Title
	}
	return apiErr
}

// HasCode reports whether err is an APIError with the given problem code. It
// lets callers branch on a specific backend code without unpacking the error.
func HasCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}
