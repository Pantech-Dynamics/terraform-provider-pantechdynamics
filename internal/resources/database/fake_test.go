package database

import (
	"context"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory databaseAPI. Every change takes effect at once, and a
// non-nil *Err is returned by the matching method.
type fakeAPI struct {
	dbs []client.Database

	createErr, getErr, listErr, deleteErr, orderErr, rulesErr, passwordErr, resizeErr error
	createState                                                                       string

	nextID      int
	creates     int
	deletes     int
	starts      int
	stops       int
	lastCreate  client.CreateDatabaseRequest
	lastRules   []string
	lastGroups  []string
	groupCalls  int
	ruleCalls   int
	passwords   []string
	resizes     []int64
	resizeLags  bool // a resize stays pending until a WaitUntil, as one in flight does
	actionOrder []string
}

func now() *time.Time {
	t := time.Date(2026, 10, 5, 10, 0, 0, 123456000, time.UTC)
	return &t
}

func ptr[T any](v T) *T { return &v }

func (f *fakeAPI) find(id string) *client.Database {
	for i := range f.dbs {
		if f.dbs[i].ID == id {
			return &f.dbs[i]
		}
	}
	return nil
}

func (f *fakeAPI) CreateDatabase(_ context.Context, req client.CreateDatabaseRequest) (*client.DatabaseOrderReference, error) {
	f.creates++
	f.lastCreate = req
	f.actionOrder = append(f.actionOrder, "create")
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("db_%d", f.nextID)
	state := f.createState
	if state == "" {
		state = client.DatabaseRunning
	}
	admin := req.AdminUsername
	if admin == "" {
		admin = client.DefaultDatabaseAdminUsername
	}
	rules := []client.DatabaseAccessRule{{CIDR: "10.0.0.0/16", Protocol: "tcp", Port: 5432}}
	if req.AccessRules != nil {
		rules = toRules(*req.AccessRules)
	}
	f.dbs = append(f.dbs, client.Database{
		ID: id, Name: req.Name, Engine: req.Engine, Version: req.Version, Port: 5432, PlanID: "plan_1",
		DataVolumeSizeGB: storageOr(req.StorageGB, 20), ZoneID: req.ZoneID, Hostname: ptr("db-1.af-abj-2.db.pantechdynamics.com"),
		PrivateIP: ptr("10.0.1.5"), AdminUsername: admin, DesiredState: client.DatabaseRunning, ObservedState: state,
		Generation: 1, ObservedGeneration: 1, AccessRules: rules, CreatedAt: now(), UpdatedAt: now(),
	})
	if req.SubnetID != "" {
		f.dbs[len(f.dbs)-1].SubnetID = ptr(req.SubnetID)
	}
	return &client.DatabaseOrderReference{OrderID: "ord_" + id, DatabaseID: id, Status: client.OrderAwaitingPayment, AdminUsername: admin}, nil
}

func storageOr(gb *int64, def int64) int64 {
	if gb != nil {
		return *gb
	}
	return def
}

// ResizeDatabaseStorage behaves like the API: the database must be running and
// the disk only grows.
func (f *fakeAPI) ResizeDatabaseStorage(_ context.Context, id string, storageGB int64) (*client.OperationReference, error) {
	f.resizes = append(f.resizes, storageGB)
	f.actionOrder = append(f.actionOrder, "resize")
	if f.resizeErr != nil {
		return nil, f.resizeErr
	}
	db := f.find(id)
	if db.ObservedState != client.DatabaseRunning {
		return nil, &client.APIError{Status: 409, Code: client.CodeInvalidResourceState, Detail: "The database must be running."}
	}
	if storageGB <= db.DataVolumeSizeGB {
		return nil, &client.APIError{Status: 422, Code: "VALIDATION_FAILED", Detail: "The request is invalid.",
			Errors: []client.FieldError{{Field: "storage_gb", Code: "INVALID_DATABASE_STORAGE", Message: "must be larger than the current size"}}}
	}
	if f.resizeLags {
		db.PendingDataVolumeSizeGB = ptr(storageGB)
	} else {
		db.DataVolumeSizeGB = storageGB
	}
	return &client.OperationReference{OperationID: "op_resize", ResourceID: id}, nil
}

func toRules(cidrs []string) []client.DatabaseAccessRule {
	rules := make([]client.DatabaseAccessRule, 0, len(cidrs))
	for _, c := range cidrs {
		rules = append(rules, client.DatabaseAccessRule{CIDR: c, Protocol: "tcp", Port: 5432})
	}
	return rules
}

func (f *fakeAPI) GetDatabase(_ context.Context, id string) (*client.Database, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if db := f.find(id); db != nil {
		cp := *db
		return &cp, nil
	}
	return nil, fmt.Errorf("getting database: %w", client.ErrNotFound)
}

func (f *fakeAPI) ListDatabases(context.Context) ([]client.Database, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]client.Database(nil), f.dbs...), nil
}

func (f *fakeAPI) DeleteDatabase(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	db := f.find(id)
	if db == nil {
		return nil, fmt.Errorf("deleting: %w", client.ErrNotFound)
	}
	db.DesiredState, db.ObservedState = client.DatabaseDeleted, client.DatabaseDeleted
	return &client.OperationReference{OperationID: "op_del", ResourceID: id}, nil
}

func (f *fakeAPI) StartDatabase(_ context.Context, id string) (*client.OperationReference, error) {
	f.starts++
	f.actionOrder = append(f.actionOrder, "start")
	db := f.find(id)
	db.DesiredState, db.ObservedState = client.DatabaseRunning, client.DatabaseRunning
	return &client.OperationReference{OperationID: "op_start", ResourceID: id}, nil
}

func (f *fakeAPI) StopDatabase(_ context.Context, id string) (*client.OperationReference, error) {
	f.stops++
	f.actionOrder = append(f.actionOrder, "stop")
	db := f.find(id)
	db.DesiredState, db.ObservedState = client.DatabaseStopped, client.DatabaseStopped
	return &client.OperationReference{OperationID: "op_stop", ResourceID: id}, nil
}

func (f *fakeAPI) ReplaceDatabaseAccessRules(_ context.Context, id string, cidrs []string) (*client.OperationReference, error) {
	f.ruleCalls++
	f.lastRules = cidrs
	f.actionOrder = append(f.actionOrder, "rules")
	if f.rulesErr != nil {
		return nil, f.rulesErr
	}
	db := f.find(id)
	db.AccessRules = toRules(cidrs)
	db.Generation++
	db.ObservedGeneration = db.Generation
	return &client.OperationReference{OperationID: "op_rules", ResourceID: id}, nil
}

func (f *fakeAPI) SetDatabaseSecurityGroups(_ context.Context, id string, groupIDs []string) (*client.OperationReference, error) {
	f.groupCalls++
	f.lastGroups = groupIDs
	f.actionOrder = append(f.actionOrder, "groups")
	db := f.find(id)
	db.SecurityGroupIDs = groupIDs
	db.EffectiveAccessRules = nil
	for _, r := range db.AccessRules {
		db.EffectiveAccessRules = append(db.EffectiveAccessRules, client.DatabaseEffectiveAccessRule{CIDR: r.CIDR, Protocol: "tcp", Port: 5432, Source: "access_rule"})
	}
	for _, g := range groupIDs {
		db.EffectiveAccessRules = append(db.EffectiveAccessRules, client.DatabaseEffectiveAccessRule{CIDR: "192.168.0.0/24", Protocol: "tcp", Port: 5432, Source: "security_group:" + g})
		db.IgnoredSecurityGroupRules = append(db.IgnoredSecurityGroupRules, client.DatabaseIgnoredSecurityGroupRule{SecurityGroupID: g, Direction: "egress", Protocol: "all", CIDR: "0.0.0.0/0", Reason: "egress_rule"})
	}
	db.Generation++
	db.ObservedGeneration = db.Generation
	return &client.OperationReference{OperationID: "op_groups", ResourceID: id}, nil
}

func (f *fakeAPI) ChangeDatabasePassword(_ context.Context, id, password string) (*client.OperationReference, error) {
	f.passwords = append(f.passwords, password)
	f.actionOrder = append(f.actionOrder, "password")
	if f.passwordErr != nil {
		return nil, f.passwordErr
	}
	db := f.find(id)
	if db.ObservedState != client.DatabaseRunning {
		return nil, fmt.Errorf("database must be running")
	}
	db.Generation++
	db.ObservedGeneration = db.Generation
	return &client.OperationReference{OperationID: "op_pw", ResourceID: id}, nil
}

func (f *fakeAPI) WaitForDatabaseOrder(_ context.Context, id string) (*client.DatabaseOrder, error) {
	if f.orderErr != nil {
		return nil, f.orderErr
	}
	return &client.DatabaseOrder{ID: id, Status: client.OrderProvisioned, OperationID: ptr("op_create")}, nil
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	f.finishResizes()
	return kittest.Wait(ctx, done, true)
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	f.finishResizes()
	return kittest.Wait(ctx, done, true)
}

// finishResizes completes a resize in flight, as time passing while we wait does.
func (f *fakeAPI) finishResizes() {
	for i := range f.dbs {
		if p := f.dbs[i].PendingDataVolumeSizeGB; p != nil {
			f.dbs[i].DataVolumeSizeGB, f.dbs[i].PendingDataVolumeSizeGB = *p, nil
		}
	}
}
