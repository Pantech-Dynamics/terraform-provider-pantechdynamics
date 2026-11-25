package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Pantech Migrate: read-only connections to another cloud's account. AWS,
// Google Cloud, Azure and Huawei Cloud are federated (they trust short-lived
// tokens Pantech signs for one connection); DigitalOcean, Hetzner, Linode,
// Vultr, Contabo and OpenStack are read by the Pantech Migrate agent the
// customer runs. Scans, plans and moves are one-shot jobs and have no
// resource here.

const (
	// CodeCloudConnectionNotFound is the 404 for a connection that is not in
	// the organization. It counts as ErrNotFound.
	CodeCloudConnectionNotFound = "CLOUD_CONNECTION_NOT_FOUND"
	// CodeMigrateUnavailable is the 503 when Migrate is not offered on this
	// deployment.
	CodeMigrateUnavailable = "MIGRATE_UNAVAILABLE"

	// Cloud connection statuses.
	CloudConnectionAwaitingRole  = "awaiting_role"
	CloudConnectionAwaitingAgent = "awaiting_agent"
	CloudConnectionVerifying     = "verifying"
	CloudConnectionConnected     = "connected"
	CloudConnectionFailed        = "failed"

	// CloudAccessFederated and CloudAccessAgent say how Pantech reads a cloud.
	CloudAccessFederated = "federated"
	CloudAccessAgent     = "agent"
)

// CloudProviders are the clouds a connection can name.
var CloudProviders = []string{"aws", "gcp", "azure", "huawei", "digitalocean", "hetzner", "linode", "vultr", "contabo", "openstack"}

// CloudProvider is a cloud Pantech Migrate can connect.
type CloudProvider struct {
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Access         string `json:"access"`
	CostReadable   bool   `json:"cost_readable"`
	PeopleReadable bool   `json:"people_readable"`
	Available      bool   `json:"available"`
}

// CloudFailure is why a connection's last check failed.
type CloudFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CloudSetupFile is a further file of a connection's setup.
type CloudSetupFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// CloudAgentSetup is how to run the Pantech Migrate agent.
type CloudAgentSetup struct {
	Image            string   `json:"image"`
	DownloadURL      *string  `json:"download_url"`
	TokenVariables   []string `json:"token_variables"`
	TokenHowTo       string   `json:"token_how_to"`
	EnrollCommand    string   `json:"enroll_command"`
	RunCommand       string   `json:"run_command"`
	ContainerCommand string   `json:"container_command"`
}

// CloudConnectionSetup is what the customer runs in their own cloud.
type CloudConnectionSetup struct {
	Access         string           `json:"access"`
	IssuerURL      *string          `json:"issuer_url"`
	Audience       *string          `json:"audience"`
	Subject        string           `json:"subject"`
	QuickCreateURL *string          `json:"quick_create_url"`
	Template       string           `json:"template"`
	TemplateFormat string           `json:"template_format"`
	Files          []CloudSetupFile `json:"files"`
	Permissions    []string         `json:"permissions"`
	Agent          *CloudAgentSetup `json:"agent"`
}

// CloudGCPFederation is what the Google Cloud setup printed.
type CloudGCPFederation struct {
	ProjectNumber       string   `json:"project_number"`
	PoolID              string   `json:"pool_id"`
	ProviderID          string   `json:"provider_id"`
	ServiceAccountEmail string   `json:"service_account_email"`
	ProjectIDs          []string `json:"project_ids"`
}

// CloudAzureFederation is what the Azure setup printed.
type CloudAzureFederation struct {
	TenantID        string   `json:"tenant_id"`
	ClientID        string   `json:"client_id"`
	SubscriptionIDs []string `json:"subscription_ids"`
}

// CloudHuaweiProject is one Huawei Cloud region project to scan.
type CloudHuaweiProject struct {
	Region string `json:"region"`
	ID     string `json:"id"`
}

// CloudHuaweiFederation is the Huawei Cloud identity provider and projects.
type CloudHuaweiFederation struct {
	IdentityProviderID string               `json:"identity_provider_id"`
	Projects           []CloudHuaweiProject `json:"projects"`
}

// CloudAgentStatus is a connection's enrolled agent.
type CloudAgentStatus struct {
	ID         string     `json:"id"`
	Online     bool       `json:"online"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// CloudConnection is a read-only connection to another cloud's account.
type CloudConnection struct {
	ID                string                 `json:"id"`
	Provider          string                 `json:"provider"`
	Name              string                 `json:"name"`
	Status            string                 `json:"status"`
	Failure           *CloudFailure          `json:"failure"`
	RoleARN           *string                `json:"role_arn"`
	AWSAccountID      *string                `json:"aws_account_id"`
	ExternalAccountID *string                `json:"external_account_id"`
	GCP               *CloudGCPFederation    `json:"gcp"`
	Azure             *CloudAzureFederation  `json:"azure"`
	Huawei            *CloudHuaweiFederation `json:"huawei"`
	Agent             *CloudAgentStatus      `json:"agent"`
	VerifiedAt        *time.Time             `json:"verified_at"`
	CreatedAt         *time.Time             `json:"created_at"`
	Setup             CloudConnectionSetup   `json:"setup"`
}

// CreateCloudConnectionRequest starts a connection.
type CreateCloudConnectionRequest struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

// UpdateCloudConnectionRequest renames a connection or sends what its setup
// printed; any of the last four queues a check.
type UpdateCloudConnectionRequest struct {
	Name    *string                `json:"name,omitempty"`
	RoleARN *string                `json:"role_arn,omitempty"`
	GCP     *CloudGCPFederation    `json:"gcp,omitempty"`
	Azure   *CloudAzureFederation  `json:"azure,omitempty"`
	Huawei  *CloudHuaweiFederation `json:"huawei,omitempty"`
	Retry   *bool                  `json:"retry,omitempty"`
}

// ListCloudProviders returns the clouds Migrate can connect.
func (c *Client) ListCloudProviders(ctx context.Context) ([]CloudProvider, error) {
	out, err := listAll[CloudProvider](ctx, c, "/cloud-providers")
	if err != nil {
		return nil, fmt.Errorf("listing cloud providers: %w", err)
	}
	return out, nil
}

// CreateCloudConnection starts a connection. It is not retried: a create is
// not verified to replay on its Idempotency-Key.
func (c *Client) CreateCloudConnection(ctx context.Context, req CreateCloudConnectionRequest) (*CloudConnection, error) {
	var conn CloudConnection
	if err := c.do(ctx, http.MethodPost, "/cloud-connections", req, &conn); err != nil {
		return nil, fmt.Errorf("creating cloud connection: %w", err)
	}
	return &conn, nil
}

// GetCloudConnection reads a connection.
func (c *Client) GetCloudConnection(ctx context.Context, id string) (*CloudConnection, error) {
	var conn CloudConnection
	if err := c.do(ctx, http.MethodGet, cloudConnectionPath(id), nil, &conn); err != nil {
		return nil, fmt.Errorf("getting cloud connection %s: %w", id, err)
	}
	return &conn, nil
}

// UpdateCloudConnection changes a connection.
func (c *Client) UpdateCloudConnection(ctx context.Context, id string, req UpdateCloudConnectionRequest) (*CloudConnection, error) {
	var conn CloudConnection
	if err := c.do(ctx, http.MethodPatch, cloudConnectionPath(id), req, &conn); err != nil {
		return nil, fmt.Errorf("updating cloud connection %s: %w", id, err)
	}
	return &conn, nil
}

// DeleteCloudConnection removes a connection and its scans. Migration plans
// made from it stay.
func (c *Client) DeleteCloudConnection(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, cloudConnectionPath(id), nil, nil); err != nil {
		return fmt.Errorf("deleting cloud connection %s: %w", id, err)
	}
	return nil
}

// WaitForCloudConnectionCheck polls a connection until its check is no longer
// running, and returns it. Callers set the deadline through ctx.
func (c *Client) WaitForCloudConnectionCheck(ctx context.Context, id string) (*CloudConnection, error) {
	var last *CloudConnection
	err := c.WaitUntil(ctx, "cloud connection "+id, func(ctx context.Context) (bool, error) {
		conn, err := c.GetCloudConnection(ctx, id)
		if err != nil {
			return false, err
		}
		last = conn
		return conn.Status != CloudConnectionVerifying, nil
	})
	return last, err
}

func cloudConnectionPath(id string) string { return "/cloud-connections/" + url.PathEscape(id) }
