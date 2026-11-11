package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the certificate endpoints return.
const (
	// CodeCertificateNotFound is the 404 for a certificate that is not in the
	// organization, including a deleted one. It counts as ErrNotFound.
	CodeCertificateNotFound = "CERTIFICATE_NOT_FOUND"

	// CodeCertificatesUnavailable is the 503 when certificates are not
	// offered on this deployment.
	CodeCertificatesUnavailable = "CERTIFICATES_UNAVAILABLE"

	// CodeCertificateInUse is the 409 for a delete or revoke of a certificate
	// a Pantech service uses.
	CodeCertificateInUse = "CERTIFICATE_IN_USE"

	// CodeCertificateNotExportable is the 409 for an export of a certificate
	// created without exportable.
	CodeCertificateNotExportable = "CERTIFICATE_NOT_EXPORTABLE"

	// CodeCertificateNotIssued is the 409 for an export before the first
	// issue.
	CodeCertificateNotIssued = "CERTIFICATE_NOT_ISSUED"

	// CodeCertificateNamesMismatch is the 422 for a reimport naming other
	// names than the certificate.
	CodeCertificateNamesMismatch = "CERTIFICATE_NAMES_MISMATCH"
)

// Certificate statuses and kinds.
const (
	CertificatePendingValidation = "pending_validation"
	CertificateIssuing           = "issuing"
	CertificateIssued            = "issued"
	CertificateRenewing          = "renewing"
	CertificateExpired           = "expired"
	CertificateRevoked           = "revoked"
	CertificateFailed            = "failed"

	CertificateKindPublic   = "public"
	CertificateKindImported = "imported"
)

// CertificateValidation is how one name is validated.
type CertificateValidation struct {
	Name        string  `json:"name"`
	Method      string  `json:"method"`
	ZoneID      *string `json:"zone_id"`
	CNAMEName   *string `json:"cname_name"`
	CNAMETarget *string `json:"cname_target"`
	Status      string  `json:"status"`
}

// CertificateUsage names a Pantech service that uses a certificate.
type CertificateUsage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Certificate is a TLS certificate: requested from a public CA through
// Pantech, or imported.
type Certificate struct {
	ID                   string                  `json:"id"`
	Kind                 string                  `json:"kind"`
	Names                []string                `json:"names"`
	CommonName           *string                 `json:"common_name"`
	Description          string                  `json:"description"`
	KeyType              string                  `json:"key_type"`
	Exportable           bool                    `json:"exportable"`
	Status               string                  `json:"status"`
	PendingReason        *string                 `json:"pending_reason"`
	FailureCode          *string                 `json:"failure_code"`
	Validations          []CertificateValidation `json:"validations"`
	ValidationDeadlineAt *time.Time              `json:"validation_deadline_at"`
	Issuer               *string                 `json:"issuer"`
	SerialNumber         *string                 `json:"serial_number"`
	FingerprintSHA256    *string                 `json:"fingerprint_sha256"`
	NotBefore            *time.Time              `json:"not_before"`
	NotAfter             *time.Time              `json:"not_after"`
	IssuedAt             *time.Time              `json:"issued_at"`
	RenewAfter           *time.Time              `json:"renew_after"`
	RenewalStatus        *string                 `json:"renewal_status"`
	InUseBy              []CertificateUsage      `json:"in_use_by"`
	RevocationPending    bool                    `json:"revocation_pending"`
	RevokedAt            *time.Time              `json:"revoked_at"`
	CreatedAt            *time.Time              `json:"created_at"`
	UpdatedAt            *time.Time              `json:"updated_at"`
	Version              int64                   `json:"version"`
}

// RequestCertificateRequest asks for a public certificate.
type RequestCertificateRequest struct {
	Names       []string `json:"names"`
	KeyType     string   `json:"key_type,omitempty"`
	Exportable  bool     `json:"exportable"`
	Description string   `json:"description,omitempty"`
}

// ImportCertificateRequest imports a certificate and its key. The key is a
// secret: never log the request.
type ImportCertificateRequest struct {
	CertificatePEM string `json:"certificate_pem"`
	ChainPEM       string `json:"chain_pem,omitempty"`
	PrivateKeyPEM  string `json:"private_key_pem"`
	Exportable     bool   `json:"exportable"`
	Description    string `json:"description,omitempty"`
}

// ReimportCertificateRequest replaces an imported certificate in place, with
// the same names.
type ReimportCertificateRequest struct {
	CertificatePEM string `json:"certificate_pem"`
	ChainPEM       string `json:"chain_pem,omitempty"`
	PrivateKeyPEM  string `json:"private_key_pem"`
}

// updateCertificateRequest changes a certificate's description.
type updateCertificateRequest struct {
	Description string `json:"description"`
}

// ExportedCertificate is a certificate with its private key. It is a secret.
type ExportedCertificate struct {
	CertificatePEM string     `json:"certificate_pem"`
	ChainPEM       string     `json:"chain_pem"`
	PrivateKeyPEM  string     `json:"private_key_pem"`
	NotAfter       *time.Time `json:"not_after"`
}

// RequestCertificate asks for a public certificate. It answers 202 with the
// certificate pending validation; a replay answers the original.
func (c *Client) RequestCertificate(ctx context.Context, req RequestCertificateRequest) (*Certificate, error) {
	var cert Certificate
	if err := c.do(ctx, http.MethodPost, "/certificates", req, &cert, replaySafe()); err != nil {
		return nil, fmt.Errorf("requesting certificate: %w", err)
	}
	return &cert, nil
}

// ImportCertificate imports a certificate. It answers 201 with it issued.
func (c *Client) ImportCertificate(ctx context.Context, req ImportCertificateRequest) (*Certificate, error) {
	var cert Certificate
	if err := c.do(ctx, http.MethodPost, "/certificates/import", req, &cert, replaySafe()); err != nil {
		return nil, fmt.Errorf("importing certificate: %w", err)
	}
	return &cert, nil
}

// GetCertificate returns one certificate, or ErrNotFound.
func (c *Client) GetCertificate(ctx context.Context, id string) (*Certificate, error) {
	var cert Certificate
	if err := c.do(ctx, http.MethodGet, certificatePath(id), nil, &cert); err != nil {
		return nil, fmt.Errorf("getting certificate %s: %w", id, err)
	}
	return &cert, nil
}

// ListCertificates returns the organization's certificates, newest first. q,
// when set, matches any part of any name.
func (c *Client) ListCertificates(ctx context.Context, q string) ([]Certificate, error) {
	path := "/certificates"
	if q != "" {
		path += "?q=" + url.QueryEscape(q)
	}
	certs, err := listAll[Certificate](ctx, c, path)
	if err != nil {
		return nil, fmt.Errorf("listing certificates: %w", err)
	}
	return certs, nil
}

// UpdateCertificateDescription changes a certificate's description.
func (c *Client) UpdateCertificateDescription(ctx context.Context, id, description string) (*Certificate, error) {
	var cert Certificate
	if err := c.do(ctx, http.MethodPatch, certificatePath(id), updateCertificateRequest{Description: description}, &cert, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating certificate %s: %w", id, err)
	}
	return &cert, nil
}

// ReimportCertificate replaces an imported certificate with another one for
// the same names.
func (c *Client) ReimportCertificate(ctx context.Context, id string, req ReimportCertificateRequest) (*Certificate, error) {
	var cert Certificate
	if err := c.do(ctx, http.MethodPost, certificatePath(id)+"/reimport", req, &cert, replaySafe()); err != nil {
		return nil, fmt.Errorf("reimporting certificate %s: %w", id, err)
	}
	return &cert, nil
}

// ExportCertificate returns the certificate with its private key. Every
// export is recorded and emails the organization's owners and admins.
func (c *Client) ExportCertificate(ctx context.Context, id string) (*ExportedCertificate, error) {
	var out ExportedCertificate
	if err := c.do(ctx, http.MethodPost, certificatePath(id)+"/export", nil, &out, replaySafe()); err != nil {
		return nil, fmt.Errorf("exporting certificate %s: %w", id, err)
	}
	return &out, nil
}

// DeleteCertificate deletes a certificate. ErrNotFound means it is already
// gone.
func (c *Client) DeleteCertificate(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, certificatePath(id), nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("deleting certificate %s: %w", id, err)
	}
	return nil
}

func certificatePath(id string) string { return "/certificates/" + url.PathEscape(id) }
