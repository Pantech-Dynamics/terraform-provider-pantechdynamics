package instance

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// ordered is the outcome of placing an order: either the order to follow, or an
// instance found after an ambiguous failure (adopted) with no order to follow.
type ordered struct {
	Order   *client.InstanceOrderReference
	Adopted *client.Instance
}

// instanceID is the id the new instance has, whichever way it was ordered.
func (o ordered) instanceID() string {
	if o.Adopted != nil {
		return o.Adopted.ID
	}
	return o.Order.InstanceID
}

// placeOrder orders the instance. Ordering spends money, so two rules apply:
//   - an instance with the same name is refused up front, because the backend
//     accepts a duplicate name, reserves the credit, and only then fails the
//     order with provisioning_handoff_failed;
//   - an ambiguous failure (the connection dropped, or a 5xx) is resolved by
//     asking the backend, never by ordering again.
func placeOrder(ctx context.Context, api instanceAPI, req client.CreateInstanceRequest) (ordered, error) {
	if err := ensureNameFree(ctx, api, req.Name); err != nil {
		return ordered{}, err
	}

	ref, err := api.CreateInstance(ctx, req)
	if err == nil {
		return ordered{Order: ref}, nil
	}
	if !isAmbiguous(ctx, err) {
		return ordered{}, err
	}

	tflog.Warn(ctx, "instance order outcome unknown, looking it up", map[string]any{"name": req.Name})
	found, lookupErr := findByName(ctx, api, req.Name)
	if lookupErr != nil {
		return ordered{}, fmt.Errorf("%w; looking the instance up afterwards also failed: %v", err, lookupErr)
	}
	if found == nil {
		return ordered{}, err
	}
	return ordered{Adopted: found}, nil
}

// ensureNameFree fails if a live instance already has this name.
func ensureNameFree(ctx context.Context, api instanceAPI, name string) error {
	existing, err := findByName(ctx, api, name)
	if err != nil {
		return fmt.Errorf("checking existing instances: %w", err)
	}
	if existing != nil {
		return fmt.Errorf("an instance named %q already exists (%s). The API would accept a second order with this name, reserve credit, and then fail it. Choose another name, or bring the existing instance under Terraform with `terraform import pantechdynamics_instance.<name> %s`", name, existing.ID, existing.ID)
	}
	return nil
}

// findByName returns the live instance with this exact name, or nil. Deleted
// instances do not count.
func findByName(ctx context.Context, api instanceAPI, name string) (*client.Instance, error) {
	instances, err := api.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	for i := range instances {
		if instances[i].Name == name && instances[i].ObservedState != client.InstanceDeleted {
			return &instances[i], nil
		}
	}
	return nil, nil
}

// isAmbiguous reports whether a failed order may still have taken effect. A
// definite answer below 500 (402, 422, 409, ...) means it did not. A cancelled
// context is not ambiguous either, since the user stopped it.
func isAmbiguous(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true // transport error: no response, outcome unknown
}
