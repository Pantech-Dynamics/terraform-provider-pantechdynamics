package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Database engines the API accepts in a create request.
const (
	EnginePostgreSQL = "postgresql"
	EngineMySQL      = "mysql"
	EngineMariaDB    = "mariadb"
)

// Database states. Desired is running, stopped or deleted. Observed adds the
// transitions and failed.
const (
	DatabasePending      = "pending"
	DatabaseProvisioning = "provisioning"
	DatabaseRunning      = "running"
	DatabaseStopping     = "stopping"
	DatabaseStopped      = "stopped"
	DatabaseDeleting     = "deleting"
	DatabaseDeleted      = "deleted"
	DatabaseFailed       = "failed"
)

// DefaultDatabaseAdminUsername is the admin login the API uses when a create
// request names none.
const DefaultDatabaseAdminUsername = "dbadmin"

// DatabaseEngineVersion is one version line of an engine, and the zones it can
// be created in.
type DatabaseEngineVersion struct {
	Version string   `json:"version"`
	EOLDate *string  `json:"eol_date"`
	Zones   []string `json:"zones"`
}

// DatabaseEngine is an engine offered for new databases.
type DatabaseEngine struct {
	Engine      string                  `json:"engine"`
	DisplayName string                  `json:"display_name"`
	Port        int64                   `json:"port"`
	Versions    []DatabaseEngineVersion `json:"versions"`
}

// DatabaseAccessRule allows TCP to the engine's port from one IPv4 CIDR.
type DatabaseAccessRule struct {
	CIDR     string `json:"cidr"`
	Protocol string `json:"protocol"`
	Port     int64  `json:"port"`
}

// DatabaseEffectiveAccessRule is one range the database accepts. Source is
// "access_rule" or "security_group:<id>".
type DatabaseEffectiveAccessRule struct {
	CIDR     string `json:"cidr"`
	Protocol string `json:"protocol"`
	Port     int64  `json:"port"`
	Source   string `json:"source"`
}

// DatabaseIgnoredSecurityGroupRule is a rule of an attached security group that
// does not apply to the database. PortRange is nil for all ports.
type DatabaseIgnoredSecurityGroupRule struct {
	SecurityGroupID string  `json:"security_group_id"`
	Direction       string  `json:"direction"`
	Protocol        string  `json:"protocol"`
	PortRange       *string `json:"port_range"`
	CIDR            string  `json:"cidr"`
	Reason          string  `json:"reason"`
}

// Database is a managed database. It never carries a password.
type Database struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Engine           string `json:"engine"`
	Version          string `json:"version"`
	Port             int64  `json:"port"`
	PlanID           string `json:"plan_id"`
	DataVolumeSizeGB int64  `json:"data_volume_size_gb"`
	// PendingDataVolumeSizeGB is the size a storage resize in flight is growing
	// the data disk to, or nil when none is running.
	PendingDataVolumeSizeGB *int64               `json:"pending_data_volume_size_gb"`
	ZoneID                  string               `json:"zone_id"`
	SubnetID                *string              `json:"subnet_id"`
	Hostname                *string              `json:"hostname"`
	PrivateIP               *string              `json:"private_ip"`
	AdminUsername           string               `json:"admin_username"`
	DesiredState            string               `json:"desired_state"`
	ObservedState           string               `json:"observed_state"`
	Generation              int64                `json:"generation"`
	ObservedGeneration      int64                `json:"observed_generation"`
	AccessRules             []DatabaseAccessRule `json:"access_rules"`

	// SecurityGroupIDs are the groups attached as extra sources of allowed
	// ranges. EffectiveAccessRules is the allow-list as applied: the access
	// rules plus the ranges the groups add. IgnoredSecurityGroupRules are the
	// groups' rules that do not apply to the database, with the reason.
	SecurityGroupIDs          []string                           `json:"security_group_ids"`
	EffectiveAccessRules      []DatabaseEffectiveAccessRule      `json:"effective_access_rules"`
	IgnoredSecurityGroupRules []DatabaseIgnoredSecurityGroupRule `json:"ignored_security_group_rules"`
	FailureCode               *string                            `json:"failure_code"`
	CreatedAt                 *time.Time                         `json:"created_at"`
	UpdatedAt                 *time.Time                         `json:"updated_at"`
}

// CreateDatabaseRequest orders a database. AccessRules nil means the zone's
// default rule, and an empty, non-nil slice means none. Password is the admin
// password: it is sent once and never stored.
type CreateDatabaseRequest struct {
	Name          string    `json:"name"`
	Engine        string    `json:"engine"`
	Version       string    `json:"version"`
	PlanSlug      string    `json:"plan_slug"`
	ZoneID        string    `json:"zone_id"`
	SubnetID      string    `json:"subnet_id,omitempty"`
	AccessRules   *[]string `json:"access_rules,omitempty"`
	AdminUsername string    `json:"admin_username,omitempty"`
	Password      string    `json:"password,omitempty"`
	// StorageGB is the data disk size; nil means the plan's disk_gb.
	StorageGB *int64 `json:"storage_gb,omitempty"`
}

// DatabaseOrderReference is the 202 body of a database create. Password is set
// only when the API generated it, and only in the response that created the
// order: a replay of the same Idempotency-Key returns it null.
type DatabaseOrderReference struct {
	OrderID          string  `json:"order_id"`
	DatabaseID       string  `json:"database_id"`
	Status           string  `json:"status"`
	OperationID      *string `json:"operation_id"`
	FailureCode      *string `json:"failure_code"`
	AmountMinor      int64   `json:"amount_minor"`
	Currency         string  `json:"currency"`
	AdminUsername    string  `json:"admin_username"`
	PasswordReturned bool    `json:"password_returned"`
	Password         *string `json:"password"`
}

// DatabaseOrder is the payment and provisioning state of a database order. Its
// statuses are the instance order's.
type DatabaseOrder struct {
	ID            string     `json:"id"`
	DatabaseID    string     `json:"database_id"`
	Status        string     `json:"status"`
	OperationID   *string    `json:"operation_id"`
	FailureCode   *string    `json:"failure_code"`
	AmountMinor   int64      `json:"amount_minor"`
	Currency      string     `json:"currency"`
	AdminUsername string     `json:"admin_username"`
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// DatabaseOrderError is returned when a database order ends in a failed state.
type DatabaseOrderError struct {
	Order DatabaseOrder
}

// Error carries the status, the failure code and the order id, so a user can
// quote them to support.
func (e *DatabaseOrderError) Error() string {
	msg := fmt.Sprintf("database order %s ended as %s", e.Order.ID, e.Order.Status)
	if e.Order.FailureCode != nil && *e.Order.FailureCode != "" {
		msg += " (" + *e.Order.FailureCode + ")"
	}
	if e.Order.Status == OrderPaymentFailed {
		msg += ": the payment was not taken, nothing was provisioned"
	} else {
		msg += ": any payment taken is returned to your credit"
	}
	return msg
}

type databaseAccessRuleRequest struct {
	CIDR string `json:"cidr"`
}

type replaceDatabaseAccessRulesRequest struct {
	Rules []databaseAccessRuleRequest `json:"rules"`
}

type setDatabaseSecurityGroupsRequest struct {
	SecurityGroupIDs []string `json:"security_group_ids"`
}

type resizeDatabaseStorageRequest struct {
	StorageGB int64 `json:"storage_gb"`
}

type changeDatabasePasswordRequest struct {
	Password string `json:"password"`
}

// ListDatabaseEngines returns the engines and version lines offered for new
// databases.
func (c *Client) ListDatabaseEngines(ctx context.Context) ([]DatabaseEngine, error) {
	engines, err := listAll[DatabaseEngine](ctx, c, "/database-engines")
	if err != nil {
		return nil, fmt.Errorf("listing database engines: %w", err)
	}
	return engines, nil
}

// CreateDatabase orders a database and returns the order to follow. It spends
// money. It is retried on gateway errors because the API replays a create on the
// same Idempotency-Key and returns the same order, so a retry cannot pay twice.
// The replay carries no generated password, which is why callers choose one.
func (c *Client) CreateDatabase(ctx context.Context, req CreateDatabaseRequest) (*DatabaseOrderReference, error) {
	var ref DatabaseOrderReference
	if err := c.do(ctx, http.MethodPost, "/databases", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating database: %w", err)
	}
	return &ref, nil
}

// GetDatabase returns one database, or ErrNotFound.
func (c *Client) GetDatabase(ctx context.Context, id string) (*Database, error) {
	var db Database
	if err := c.do(ctx, http.MethodGet, databasePath(id), nil, &db); err != nil {
		return nil, fmt.Errorf("getting database %s: %w", id, err)
	}
	return &db, nil
}

// ListDatabases returns every database, following the cursor.
func (c *Client) ListDatabases(ctx context.Context) ([]Database, error) {
	dbs, err := listAll[Database](ctx, c, "/databases")
	if err != nil {
		return nil, fmt.Errorf("listing databases: %w", err)
	}
	return dbs, nil
}

// DeleteDatabase starts deleting a database. It replays on the same key.
func (c *Client) DeleteDatabase(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, databasePath(id), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting database %s: %w", id, err)
	}
	return &ref, nil
}

// StartDatabase starts a stopped database. It replays on the same key.
func (c *Client) StartDatabase(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, databasePath(id)+"/start", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("starting database %s: %w", id, err)
	}
	return &ref, nil
}

// StopDatabase stops a running database. It replays on the same key.
func (c *Client) StopDatabase(ctx context.Context, id string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, databasePath(id)+"/stop", nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("stopping database %s: %w", id, err)
	}
	return &ref, nil
}

// ReplaceDatabaseAccessRules replaces the whole allow-list with cidrs. An empty
// list removes every rule. It replays on the same key.
func (c *Client) ReplaceDatabaseAccessRules(ctx context.Context, id string, cidrs []string) (*OperationReference, error) {
	req := replaceDatabaseAccessRulesRequest{Rules: make([]databaseAccessRuleRequest, 0, len(cidrs))}
	for _, cidr := range cidrs {
		req.Rules = append(req.Rules, databaseAccessRuleRequest{CIDR: cidr})
	}
	var ref OperationReference
	if err := c.do(ctx, http.MethodPut, databasePath(id)+"/access-rules", req, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("replacing access rules of database %s: %w", id, err)
	}
	return &ref, nil
}

// SetDatabaseSecurityGroups replaces the security groups attached to the
// database; an empty list detaches every group. A group only adds the CIDRs of
// its ingress rules that cover the engine's port. It replays on the same key.
func (c *Client) SetDatabaseSecurityGroups(ctx context.Context, id string, groupIDs []string) (*OperationReference, error) {
	if groupIDs == nil {
		groupIDs = []string{}
	}
	var ref OperationReference
	if err := c.do(ctx, http.MethodPut, databasePath(id)+"/security-groups", setDatabaseSecurityGroupsRequest{SecurityGroupIDs: groupIDs}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("setting security groups of database %s: %w", id, err)
	}
	return &ref, nil
}

// ChangeDatabasePassword sets the admin password. The database must be running.
// The password is never logged: errors name only the database.
func (c *Client) ChangeDatabasePassword(ctx context.Context, id, password string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPut, databasePath(id)+"/password", changeDatabasePasswordRequest{Password: password}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("changing the password of database %s: %w", id, err)
	}
	return &ref, nil
}

// ResizeDatabaseStorage grows the data disk to storageGB. The disk only grows,
// the database must be running, and one resize runs at a time. It replays on
// the same key.
func (c *Client) ResizeDatabaseStorage(ctx context.Context, id string, storageGB int64) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, databasePath(id)+"/resize-storage", resizeDatabaseStorageRequest{StorageGB: storageGB}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("resizing the storage of database %s: %w", id, err)
	}
	return &ref, nil
}

// CreateDatabaseSnapshot starts a crash-consistent snapshot of the database's
// data disk. It replays on the same key.
func (c *Client) CreateDatabaseSnapshot(ctx context.Context, databaseID, name string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodPost, databasePath(databaseID)+"/snapshots", createSnapshotRequest{Name: name}, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating snapshot %q of database %s: %w", name, databaseID, err)
	}
	return &ref, nil
}

// GetDatabaseSnapshot returns one snapshot of the database, or ErrNotFound. A
// snapshot of anything else is not found here.
func (c *Client) GetDatabaseSnapshot(ctx context.Context, databaseID, snapshotID string) (*Snapshot, error) {
	var snap Snapshot
	if err := c.do(ctx, http.MethodGet, databaseSnapshotPath(databaseID, snapshotID), nil, &snap); err != nil {
		return nil, fmt.Errorf("getting snapshot %s of database %s: %w", snapshotID, databaseID, err)
	}
	return &snap, nil
}

// ListDatabaseSnapshots returns the database's snapshots, following the cursor.
func (c *Client) ListDatabaseSnapshots(ctx context.Context, databaseID string) ([]Snapshot, error) {
	snaps, err := listAll[Snapshot](ctx, c, databasePath(databaseID)+"/snapshots")
	if err != nil {
		return nil, fmt.Errorf("listing snapshots of database %s: %w", databaseID, err)
	}
	return snaps, nil
}

// DeleteDatabaseSnapshot starts deleting one snapshot of the database. It
// replays on the same key.
func (c *Client) DeleteDatabaseSnapshot(ctx context.Context, databaseID, snapshotID string) (*OperationReference, error) {
	var ref OperationReference
	if err := c.do(ctx, http.MethodDelete, databaseSnapshotPath(databaseID, snapshotID), nil, &ref, replaySafe()); err != nil {
		return nil, fmt.Errorf("deleting snapshot %s of database %s: %w", snapshotID, databaseID, err)
	}
	return &ref, nil
}

// GetDatabaseOrder returns an order, or ErrNotFound.
func (c *Client) GetDatabaseOrder(ctx context.Context, id string) (*DatabaseOrder, error) {
	var order DatabaseOrder
	if err := c.do(ctx, http.MethodGet, "/database-orders/"+url.PathEscape(id), nil, &order); err != nil {
		return nil, fmt.Errorf("getting database order %s: %w", id, err)
	}
	return &order, nil
}

// WaitForDatabaseOrder polls until the order is provisioned, and returns it so
// the caller can follow its operation. A failed or declined order is returned as
// a *DatabaseOrderError. Callers set the deadline through ctx.
func (c *Client) WaitForDatabaseOrder(ctx context.Context, id string) (*DatabaseOrder, error) {
	var settled *DatabaseOrder
	err := c.pollUntil(ctx, "database order "+id, func(ctx context.Context) (bool, string, error) {
		order, err := c.GetDatabaseOrder(ctx, id)
		if err != nil {
			return false, "", err
		}
		switch order.Status {
		case OrderProvisioned:
			settled = order
			return true, order.Status, nil
		case OrderFailed, OrderPaymentFailed:
			return false, order.Status, &DatabaseOrderError{Order: *order}
		}
		return false, order.Status, nil
	})
	if err != nil {
		return nil, err
	}
	return settled, nil
}

// databasePath escapes the id so a malformed value cannot alter the URL path.
func databasePath(id string) string {
	return "/databases/" + url.PathEscape(id)
}

// databaseSnapshotPath escapes both ids so a malformed value cannot alter the path.
func databaseSnapshotPath(databaseID, snapshotID string) string {
	return databasePath(databaseID) + "/snapshots/" + url.PathEscape(snapshotID)
}
