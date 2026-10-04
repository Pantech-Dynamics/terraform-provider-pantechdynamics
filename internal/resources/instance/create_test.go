package instance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var orderReq = client.CreateInstanceRequest{Name: "web", PlanSlug: "individual", ImageSlug: "ubuntu-24-04"}

// Ordering spends money, so these tests pin down exactly when an order is sent.
func TestPlaceOrder(t *testing.T) {
	deleted := running("vm_old", "web")
	deleted.ObservedState = client.InstanceDeleted

	tests := []struct {
		name         string
		api          *fakeAPI
		wantCreates  int
		wantOrder    bool
		wantAdopted  string
		wantErrText  string
		wantErrIsAPI int // status of an expected APIError, 0 for none
	}{
		{
			name: "success", api: &fakeAPI{}, wantCreates: 1, wantOrder: true,
		},
		{
			name: "a deleted instance with the same name does not block", api: &fakeAPI{instances: []client.Instance{deleted}}, wantCreates: 1, wantOrder: true,
		},
		{
			name: "a live instance with the same name is refused before ordering",
			api:  &fakeAPI{instances: []client.Instance{running("vm_7", "web")}}, wantCreates: 0, wantErrText: "already exists",
		},
		{
			name: "a listing failure stops the order", api: &fakeAPI{listErr: errConnReset}, wantCreates: 0, wantErrText: "checking existing instances",
		},
		{
			name: "no credit is a definite answer and is never looked up",
			api:  &fakeAPI{createErr: &client.APIError{Status: 402, Code: client.CodeInsufficientCredit}}, wantCreates: 1, wantErrIsAPI: 402,
		},
		{
			name: "validation failure is a definite answer",
			api:  &fakeAPI{createErr: &client.APIError{Status: 422, Code: "VALIDATION_FAILED"}}, wantCreates: 1, wantErrIsAPI: 422,
		},
		{
			name: "a lost response is adopted, never re-ordered",
			api:  &fakeAPI{createErr: errConnReset, createLands: true}, wantCreates: 1, wantAdopted: "vm_1",
		},
		{
			name: "a 5xx whose order landed is adopted",
			api:  &fakeAPI{createErr: &client.APIError{Status: 502}, createLands: true}, wantCreates: 1, wantAdopted: "vm_1",
		},
		{
			name: "an ambiguous failure with nothing found returns the cause",
			api:  &fakeAPI{createErr: errConnReset}, wantCreates: 1, wantErrText: "connection reset",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := placeOrder(context.Background(), tt.api, orderReq)

			if tt.api.creates != tt.wantCreates {
				t.Fatalf("creates = %d, want %d: an order must never be re-sent", tt.api.creates, tt.wantCreates)
			}
			switch {
			case tt.wantOrder:
				if err != nil || got.Order == nil || got.instanceID() != "vm_1" {
					t.Fatalf("got = %+v, err = %v", got, err)
				}
			case tt.wantAdopted != "":
				if err != nil || got.Adopted == nil || got.Order != nil || got.instanceID() != tt.wantAdopted {
					t.Fatalf("got = %+v, err = %v", got, err)
				}
			case tt.wantErrIsAPI != 0:
				var apiErr *client.APIError
				if !errors.As(err, &apiErr) || apiErr.Status != tt.wantErrIsAPI || tt.api.lists != 1 {
					t.Fatalf("err = %v, lists = %d: a definite failure must not trigger a lookup", err, tt.api.lists)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("err = %v, want %q", err, tt.wantErrText)
				}
			}
		})
	}
}

func TestPlaceOrderLookupFailureKeepsTheCause(t *testing.T) {
	api := &fakeAPI{createErr: errConnReset}
	calls := 0
	wrapped := &listFailsAfterFirst{fakeAPI: api, calls: &calls}

	_, err := placeOrder(context.Background(), wrapped, orderReq)

	if err == nil || !strings.Contains(err.Error(), "connection reset") || !strings.Contains(err.Error(), "looking the instance up afterwards also failed") {
		t.Fatalf("err = %v", err)
	}
}

// listFailsAfterFirst lets the pre-order check pass and fails the lookup after.
type listFailsAfterFirst struct {
	*fakeAPI
	calls *int
}

func (l *listFailsAfterFirst) ListInstances(ctx context.Context) ([]client.Instance, error) {
	*l.calls++
	if *l.calls > 1 {
		return nil, errors.New("list also failed")
	}
	return l.fakeAPI.ListInstances(ctx)
}

func TestPlaceOrderCancelledContextIsNotLookedUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeAPI{createErr: context.Canceled, createLands: true}
	wrapped := &cancelAfterCheck{fakeAPI: api, cancel: cancel}

	_, err := placeOrder(ctx, wrapped, orderReq)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if api.lists != 1 {
		t.Fatalf("lists = %d: only the pre-order check, no lookup after a cancel", api.lists)
	}
}

// cancelAfterCheck cancels the context once the pre-order check has run.
type cancelAfterCheck struct {
	*fakeAPI
	cancel context.CancelFunc
}

func (c *cancelAfterCheck) ListInstances(ctx context.Context) ([]client.Instance, error) {
	out, err := c.fakeAPI.ListInstances(ctx)
	c.cancel()
	return out, err
}
