package volume

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

func TestCheckOffering(t *testing.T) {
	offerings := defaultOfferings()
	tests := []struct {
		name      string
		slug      string
		size      int64
		sizeSet   bool
		wantField string // "" when valid
		wantText  string
	}{
		{"fixed offering, no size", "small-5gb", 0, false, "", ""},
		{"fixed offering, matching size", "small-5gb", 5, true, "", ""},
		{"customized offering with a size", "custom", 50, true, "", ""},
		{"unknown offering", "nope", 0, false, "disk_offering_slug", "pantechdynamics_disk_offerings"},
		{"customized offering without a size", "custom", 0, false, "size_gb", "required"},
		{"fixed offering with a different size", "small-5gb", 50, true, "size_gb", "would ignore"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			off, problem := checkOffering(offerings, tt.slug, tt.size, tt.sizeSet)
			if tt.wantField == "" {
				if problem != nil || off == nil || off.Slug != tt.slug {
					t.Fatalf("off = %+v, problem = %+v", off, problem)
				}
				return
			}
			if problem == nil || problem.Field != tt.wantField || !strings.Contains(problem.Message, tt.wantText) {
				t.Fatalf("problem = %+v, want field %q mentioning %q", problem, tt.wantField, tt.wantText)
			}
		})
	}
}

func TestTargetSize(t *testing.T) {
	custom := client.DiskOffering{Slug: "custom", CustomSize: true}
	fixed := client.DiskOffering{Slug: "small-5gb", SizeGB: i64(5)}
	if got := targetSize(&custom, 50); got != 50 {
		t.Fatalf("custom = %d", got)
	}
	if got := targetSize(&fixed, 0); got != 5 {
		t.Fatalf("fixed = %d: a fixed offering has its own size", got)
	}
}

// Ordering a volume bills it by the hour, so these tests pin down when an order is sent.
func TestPlaceVolume(t *testing.T) {
	req := client.CreateVolumeRequest{Name: "data", DiskOfferingSlug: "small-5gb"}
	deleted := volume("vol_old", "data", "small-5gb", 5, "shared")
	deleted.ObservedState = client.VolumeDeleted

	tests := []struct {
		name        string
		api         *fakeAPI
		wantCreates int
		wantID      string
		wantOp      bool
		wantErrText string
		wantAPI     int // status of an expected APIError
	}{
		{name: "success", api: seeded(), wantCreates: 1, wantID: "vol_1", wantOp: true},
		{name: "a deleted volume with the same name does not block", api: seeded(deleted), wantCreates: 1, wantID: "vol_2", wantOp: true},
		{name: "a live volume with the same name is refused before ordering",
			api: seeded(volume("vol_7", "data", "small-5gb", 5, "shared")), wantCreates: 0, wantErrText: "already exists"},
		{name: "a listing failure stops the order", api: func() *fakeAPI { f := seeded(); f.listErr = errConnReset; return f }(), wantCreates: 0, wantErrText: "checking existing volumes"},
		{name: "a validation failure is definite",
			api: func() *fakeAPI {
				f := seeded()
				f.createErr = &client.APIError{Status: 422, Code: "VALIDATION_FAILED"}
				return f
			}(), wantCreates: 1, wantAPI: 422},
		{name: "a lost response is adopted, never re-ordered",
			api: func() *fakeAPI { f := seeded(); f.createErr = errConnReset; f.createLands = true; return f }(), wantCreates: 1, wantID: "vol_1"},
		{name: "a 5xx whose order landed is adopted",
			api: func() *fakeAPI {
				f := seeded()
				f.createErr = &client.APIError{Status: 502}
				f.createLands = true
				return f
			}(), wantCreates: 1, wantID: "vol_1"},
		{name: "an ambiguous failure with nothing found returns the cause",
			api: func() *fakeAPI { f := seeded(); f.createErr = errConnReset; return f }(), wantCreates: 1, wantErrText: "connection reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := placeVolume(ctx, tt.api, req, "")

			if tt.api.creates != tt.wantCreates {
				t.Fatalf("creates = %d, want %d: an order must never be re-sent", tt.api.creates, tt.wantCreates)
			}
			switch {
			case tt.wantID != "":
				if err != nil || got.VolumeID != tt.wantID || (got.OperationID != "") != tt.wantOp {
					t.Fatalf("got = %+v, err = %v", got, err)
				}
			case tt.wantAPI != 0:
				var apiErr *client.APIError
				if !errors.As(err, &apiErr) || apiErr.Status != tt.wantAPI {
					t.Fatalf("err = %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("err = %v, want %q", err, tt.wantErrText)
				}
			}
		})
	}
}

func TestIsAmbiguous(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"transport error", ctx, errConnReset, true},
		{"502", ctx, &client.APIError{Status: 502}, true},
		{"500", ctx, &client.APIError{Status: 500}, true},
		{"422 is definite", ctx, &client.APIError{Status: 422}, false},
		{"409 is definite", ctx, &client.APIError{Status: 409}, false},
		{"a cancelled context is not ambiguous", cancelled, errConnReset, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAmbiguous(tt.ctx, tt.err); got != tt.want {
				t.Fatalf("isAmbiguous = %v, want %v", got, tt.want)
			}
		})
	}
}
