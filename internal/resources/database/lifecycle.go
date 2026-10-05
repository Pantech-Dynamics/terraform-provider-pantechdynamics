package database

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// ordered is the outcome of placing an order: either the order to follow, or a
// database found after an ambiguous failure (adopted) with no order to follow.
type ordered struct {
	Order   *client.DatabaseOrderReference
	Adopted *client.Database
}

func (o ordered) databaseID() string {
	if o.Adopted != nil {
		return o.Adopted.ID
	}
	return o.Order.DatabaseID
}

func (o ordered) adminUsername() string {
	if o.Adopted != nil {
		return o.Adopted.AdminUsername
	}
	return o.Order.AdminUsername
}

// placeOrder orders the database. Ordering spends money, so two rules apply, as
// for instances:
//   - a database with the same name is refused up front, because the order would
//     take the payment and then fail with database_name_taken;
//   - an ambiguous failure (the connection dropped, or a 5xx after the client's
//     own retries) is resolved by looking the database up, never by ordering again.
//
// The generated password the API can return is never read: password_wo is
// required, so the API has none to generate.
func placeOrder(ctx context.Context, api databaseAPI, req client.CreateDatabaseRequest) (ordered, error) {
	if err := ensureNameFree(ctx, api, req.Name); err != nil {
		return ordered{}, err
	}

	ref, err := api.CreateDatabase(ctx, req)
	if err == nil {
		return ordered{Order: ref}, nil
	}
	if !isAmbiguous(ctx, err) {
		return ordered{}, err
	}

	tflog.Warn(ctx, "database order outcome unknown, looking it up", map[string]any{"name": req.Name})
	found, lookupErr := findByName(ctx, api, req.Name)
	if lookupErr != nil {
		return ordered{}, fmt.Errorf("%w; looking the database up afterwards also failed: %v", err, lookupErr)
	}
	if found == nil {
		return ordered{}, err
	}
	return ordered{Adopted: found}, nil
}

// ensureNameFree fails if a live database already has this name.
func ensureNameFree(ctx context.Context, api databaseAPI, name string) error {
	existing, err := findByName(ctx, api, name)
	if err != nil {
		return fmt.Errorf("checking existing databases: %w", err)
	}
	if existing != nil {
		return fmt.Errorf("a database named %q already exists (%s). An order with this name would be paid for and then fail. Choose another name, or bring the existing database under Terraform with `terraform import pantechdynamics_database.<name> %s`", name, existing.ID, existing.ID)
	}
	return nil
}

// findByName returns the live database with this exact name, or nil.
func findByName(ctx context.Context, api databaseAPI, name string) (*client.Database, error) {
	dbs, err := api.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}
	for i := range dbs {
		if dbs[i].Name == name && dbs[i].ObservedState != client.DatabaseDeleted {
			return &dbs[i], nil
		}
	}
	return nil, nil
}

// isAmbiguous reports whether a failed order may still have taken effect. A
// definite answer below 500 (402, 409, 422, ...) means it did not.
func isAmbiguous(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true
}

// waitFor follows an operation when there is one, and otherwise waits on the
// database alone. done is the authority either way.
func (r *Resource) waitFor(ctx context.Context, opID, what string, done client.DoneCheck) error {
	if opID == "" {
		return r.api.WaitUntil(ctx, what, done)
	}
	return r.api.WaitForOperation(ctx, opID, done)
}

// hasObservedState finishes when the database reaches want. A failed database
// ends the wait at once, with its failure code.
func (r *Resource) hasObservedState(id, want string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		db, err := r.api.GetDatabase(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return false, nil // not visible yet
		}
		if err != nil {
			return false, err
		}
		if db.ObservedState == client.DatabaseFailed {
			return false, failedError(db)
		}
		return db.ObservedState == want, nil
	}
}

// isGone finishes a delete when the database is gone or reported as deleted.
func (r *Resource) isGone(id string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		db, err := r.api.GetDatabase(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return db.ObservedState == client.DatabaseDeleted, nil
	}
}

// applied finishes a change of access rules or password when the platform has
// applied every change made after generation before: generation has moved on,
// and observed_generation has caught up with it.
func (r *Resource) applied(id string, before int64) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		db, err := r.api.GetDatabase(ctx, id)
		if err != nil {
			return false, err
		}
		if db.ObservedState == client.DatabaseFailed {
			return false, failedError(db)
		}
		return db.Generation > before && db.ObservedGeneration >= db.Generation, nil
	}
}

// applyPower brings the database to target, "running" or "stopped". It reads the
// database first and sends an action only when it is in the opposite settled
// state, so a redundant start or stop is never sent.
func (r *Resource) applyPower(ctx context.Context, id, target string) error {
	db, err := r.settled(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case db.ObservedState == target:
		return nil
	case db.ObservedState == client.DatabaseFailed:
		return failedError(db)
	case db.ObservedState != client.DatabaseRunning && db.ObservedState != client.DatabaseStopped:
		return fmt.Errorf("database %s is %q, which cannot be changed to %q", id, db.ObservedState, target)
	}

	var ref *client.OperationReference
	if target == client.DatabaseStopped {
		ref, err = r.api.StopDatabase(ctx, id)
	} else {
		ref, err = r.api.StartDatabase(ctx, id)
	}
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.hasObservedState(id, target))
}

// settled reads the database, first waiting out a transition.
func (r *Resource) settled(ctx context.Context, id string) (*client.Database, error) {
	db, err := r.api.GetDatabase(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isTransitional(db.ObservedState) {
		return db, nil
	}
	if db.ObservedState == client.DatabaseDeleting {
		return nil, fmt.Errorf("database %s is being deleted", id)
	}
	err = r.api.WaitUntil(ctx, "database "+id+" to settle", func(ctx context.Context) (bool, error) {
		current, err := r.api.GetDatabase(ctx, id)
		if err != nil {
			return false, err
		}
		db = current
		return !isTransitional(current.ObservedState), nil
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

func isTransitional(state string) bool {
	switch state {
	case client.DatabasePending, client.DatabaseProvisioning, client.DatabaseStopping, client.DatabaseDeleting:
		return true
	}
	return false
}

// replaceAccessRules sends the whole allow-list and waits until it is applied.
func (r *Resource) replaceAccessRules(ctx context.Context, id string, cidrs []string) error {
	before, err := r.api.GetDatabase(ctx, id)
	if err != nil {
		return err
	}
	ref, err := r.api.ReplaceDatabaseAccessRules(ctx, id, cidrs)
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.applied(id, before.Generation))
}

// setSecurityGroups sends the whole set of attached groups and waits until the
// resulting allow-list is applied.
func (r *Resource) setSecurityGroups(ctx context.Context, id string, groupIDs []string) error {
	before, err := r.api.GetDatabase(ctx, id)
	if err != nil {
		return err
	}
	ref, err := r.api.SetDatabaseSecurityGroups(ctx, id, groupIDs)
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.applied(id, before.Generation))
}

// changePassword sets the admin password and waits until it is applied. The
// password is never logged.
func (r *Resource) changePassword(ctx context.Context, id, password string) error {
	before, err := r.api.GetDatabase(ctx, id)
	if err != nil {
		return err
	}
	ref, err := r.api.ChangeDatabasePassword(ctx, id, password)
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.applied(id, before.Generation))
}

// failedError explains a failed database with the platform's failure code.
func failedError(db *client.Database) error {
	msg := fmt.Sprintf("database %s is in the failed state", db.ID)
	hint := ""
	if db.FailureCode != nil && *db.FailureCode != "" {
		msg += " (" + *db.FailureCode + ")"
		if h := resourcekit.FailureCodeHint(*db.FailureCode); h != "" {
			hint = " " + h
		}
	}
	return errors.New(msg + ". Delete it and create it again, for example with `terraform apply -replace=<address>`." + hint)
}
