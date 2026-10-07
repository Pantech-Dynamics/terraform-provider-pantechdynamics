package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Sentinel errors let callers branch with errors.Is, much like catching a
// specific exception type in Java.
var (
	// ErrNotFound means the backend answered RESOURCE_NOT_FOUND, or a
	// resource-specific not-found code such as KUBERNETES_CLUSTER_NOT_FOUND.
	// Read removes the resource from state on it, and Delete treats it as
	// success.
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

	// codeSnapshotScheduleNotFound is the 404 for a resource that has no
	// snapshot schedule yet. It counts as "not found" like any missing resource.
	codeSnapshotScheduleNotFound = "SNAPSHOT_SCHEDULE_NOT_FOUND"
)

// notFoundCodes are the 404 codes that mean the resource in the path does not
// exist: RESOURCE_NOT_FOUND, and the resource-specific codes some endpoints
// answer instead (every 404 *_NOT_FOUND code in the public contract). Field
// codes such as PLAN_NOT_FOUND come inside a 422 VALIDATION_FAILED, never as
// the top-level code, so they cannot match. NOT_FOUND ("not offered on this
// platform") and GATEWAY_NO_ROUTE are left out on purpose: neither means the
// object is gone.
var notFoundCodes = map[string]bool{
	codeResourceNotFound:          true,
	codeSnapshotScheduleNotFound:  true,
	CodeKubernetesClusterNotFound: true,
	"DATABASE_NOT_FOUND":          true,
	"DATABASE_SNAPSHOT_NOT_FOUND": true,
	"DATABASE_ORDER_NOT_FOUND":    true,
	"RECEIPT_NOT_FOUND":           true,
}

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
	if e.Detail != "" && !e.detailRepeatsFields() {
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
	if e.Code == codeGatewayNoRoute {
		msg += ". Check base_url: it must include the /public/v1 prefix, for example https://api.pantechdynamics.com/public/v1"
	}
	return msg
}

// genericValidationDetail is the placeholder detail older backends sent on a
// 422. It says nothing the field list does not.
const genericValidationDetail = "One or more fields are invalid."

// detailRepeatsFields reports whether the detail says nothing beyond the field
// errors listed after it: the generic placeholder, or the "field: message;
// field2: message." text the backend now builds from those same errors.
// Printing both would show every field twice.
func (e *APIError) detailRepeatsFields() bool {
	if len(e.Errors) == 0 {
		return false
	}
	return e.Detail == genericValidationDetail || e.Detail == validationDetail(e.Errors)
}

// validationDetail mirrors how the backend builds a 422 detail from its field
// errors (cloud internal/platform/problem.ValidationDetail).
func validationDetail(fields []FieldError) string {
	parts := make([]string, 0, len(fields))
	for _, fe := range fields {
		message := strings.TrimSuffix(strings.TrimSpace(fe.Message), ".")
		if fe.Field == "" {
			parts = append(parts, message)
			continue
		}
		parts = append(parts, fe.Field+": "+message)
	}
	return strings.Join(parts, "; ") + "."
}

// Is maps backend codes to the sentinel errors. It branches on the problem
// code, not only the HTTP status, because a 404 can mean two different things.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		// A deleted cluster answers KUBERNETES_CLUSTER_NOT_FOUND straight away,
		// although its nodes are destroyed 12 hours later; that is gone. A
		// deleted database answers DATABASE_NOT_FOUND.
		return notFoundCodes[e.Code]
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

// HasFieldCode reports whether err is a validation failure with an entry for this
// field and code, for example plan_slug and PLAN_NOT_BIGGER. A 422 carries its
// specific reasons in the errors list, not in the top-level code.
func HasFieldCode(err error, field, code string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, fe := range apiErr.Errors {
		if fe.Field == field && fe.Code == code {
			return true
		}
	}
	return false
}
