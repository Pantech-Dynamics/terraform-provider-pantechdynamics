package resourcekit

import (
	"errors"
	"strings"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// Provisioning failure codes a failed operation, instance or database carries
// (cloud internal/provisioning/domain/failure_text.go). The backend's reason
// already says what went wrong in plain words; the hints below only add what to
// do about it in Terraform.
const (
	FailureCapacityUnavailable = "PROVISIONING_CAPACITY_UNAVAILABLE"
	FailureLimitExceeded       = "PROVISIONING_LIMIT_EXCEEDED"
	FailureAddressesExhausted  = "PROVISIONING_ADDRESSES_EXHAUSTED"
	FailureResourceBusy        = "PROVISIONING_RESOURCE_BUSY"
	FailureImageUnavailable    = "PROVISIONING_IMAGE_UNAVAILABLE"
	FailureInvalidRequest      = "PROVISIONING_INVALID_REQUEST"
	FailureResourceNotFound    = "PROVISIONING_RESOURCE_NOT_FOUND"
	FailureResourceConflict    = "PROVISIONING_RESOURCE_CONFLICT"
	FailureUnavailable         = "PROVISIONING_UNAVAILABLE"
	FailureInternalError       = "PROVISIONING_INTERNAL_ERROR"
	FailureGeneric             = "PROVISIONING_FAILED"
	FailureRetriesExhausted    = "PROVISIONING_RETRIES_EXHAUSTED"
)

const contactSupport = "Run `terraform apply` again; if it keeps failing, contact support and quote the operation id."

// failureHints maps each provisioning failure code to the next step.
var failureHints = map[string]string{
	FailureCapacityUnavailable: "Choose a different plan (plan_slug) or region, or run `terraform apply` again later.",
	FailureLimitExceeded:       "Delete resources you no longer use, or contact support to raise the limit, then run `terraform apply` again.",
	FailureAddressesExhausted:  "Run `terraform apply` again later, or choose another region.",
	FailureResourceBusy:        "Wait for the resource to settle, then run `terraform apply` again.",
	FailureImageUnavailable:    "Choose another image (image_slug) or region, or run `terraform apply` again later.",
	FailureInvalidRequest:      "Check the configuration for a setting this zone does not support, then run `terraform apply` again.",
	FailureResourceNotFound:    "Run `terraform refresh` to pick up the current state, then run `terraform apply` again.",
	FailureResourceConflict:    "Use a different name or settings, then run `terraform apply` again.",
	FailureUnavailable:         "The platform is temporarily unavailable. Run `terraform apply` again in a few minutes.",
	FailureInternalError:       "Contact support and quote the operation id.",
	FailureGeneric:             contactSupport,
	FailureRetriesExhausted:    contactSupport,
}

// legacyFailureCodes maps the adapter codes that operations recorded before
// 2026-10-05 may still carry to the code the same failure has now. Any other
// PROVISIONING_ code reads as PROVISIONING_FAILED, as the backend reads it.
var legacyFailureCodes = map[string]string{
	"PROVISIONING_CAPACITY_ERROR":    FailureCapacityUnavailable,
	"PROVISIONING_TIMEOUT":           FailureUnavailable,
	"PROVISIONING_ACCOUNT_NOT_READY": FailureUnavailable,
	"PROVISIONING_ACCOUNT_NOT_FOUND": FailureInternalError,
	"PROVISIONING_ACCOUNT_RECREATED": FailureInternalError,
}

// FailureCodeHint returns the next step for a provisioning failure code, or ""
// when the code is not one.
func FailureCodeHint(code string) string {
	if current, ok := legacyFailureCodes[code]; ok {
		code = current
	} else if strings.HasPrefix(code, "PROVISIONING_") {
		code = FailureGeneric
	}
	return failureHints[code]
}

// WithFailureHint returns err's text, followed by the next step when err is a
// failed operation with a provisioning failure code.
func WithFailureHint(err error) string {
	var opErr *client.OperationError
	if errors.As(err, &opErr) && opErr.Operation.Failure != nil {
		if hint := FailureCodeHint(opErr.Operation.Failure.Code); hint != "" {
			return err.Error() + "\n\n" + hint
		}
	}
	return err.Error()
}
