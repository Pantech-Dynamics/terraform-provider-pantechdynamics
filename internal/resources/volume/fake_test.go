package volume

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// fakeAPI is an in-memory volumeAPI that behaves like the staging platform:
//   - a shared volume cannot attach: the operation fails and leaves a stale
//     desired instance, which then blocks the delete;
//   - detaching a volume that is not attached fails its operation harmlessly and
//     clears that stale intent;
//   - delete and resize of an attached volume return 409, and so does a shrink.
//
// The counters let a test prove the provider never sends a call the platform
// would refuse (refused) and sends a detach only when it should (redundantDetaches).
type fakeAPI struct {
	volumes   []client.Volume
	offerings []client.DiskOffering

	createErr, listErr, getErr, deleteErr, offeringsErr, untilErr error
	opStuck                                                       bool
	createLands                                                   bool

	pendingOpErr error // returned by the next WaitForOperation, for example a failed operation

	nextID, creates, attaches, detaches, redundantDetaches, resizes, deletes, refused int
	lastCreate                                                                        client.CreateVolumeRequest
}

var errConnReset = errors.New("connection reset by peer")

func ptr(s string) *string { return &s }

func i64(n int64) *int64 { return &n }

func stamp() *time.Time {
	t := time.Date(2026, 10, 4, 12, 10, 12, 0, time.UTC)
	return &t
}

// defaultOfferings mirrors the staging catalog, plus local-40gb, a hypothetical
// bigger local offering that staging does not have. It lets the tests cover a
// local volume growing, which the real catalog cannot do.
func defaultOfferings() []client.DiskOffering {
	return []client.DiskOffering{
		{Slug: "custom", Name: "Custom", CustomSize: true, StorageType: "shared", Currency: "NGN", HourlyPriceMinor: 32},
		{Slug: "small-5gb", Name: "Small", SizeGB: i64(5), StorageType: "shared", Currency: "NGN", HourlyPriceMinor: 160},
		{Slug: "shared-10gb", Name: "Shared-10GB", SizeGB: i64(10), StorageType: "shared", Currency: "NGN", HourlyPriceMinor: 320},
		{Slug: "small-local-20gb", Name: "Small (Local)", SizeGB: i64(20), StorageType: "local", Currency: "NGN", HourlyPriceMinor: 640},
		{Slug: "local-40gb", Name: "Local 40", SizeGB: i64(40), StorageType: "local", Currency: "NGN", HourlyPriceMinor: 1280},
	}
}

// volume builds an active, detached volume.
func volume(id, name, offering string, sizeGB int64, storage string) client.Volume {
	return client.Volume{
		ID: id, Name: name, SizeGB: sizeGB, DiskOfferingSlug: offering, StorageType: ptr(storage),
		Region: ptr("af-abj"), Zone: ptr("af-abj-1"), MonthlyCost: &client.Money{Currency: "NGN", AmountMinor: 116800},
		DesiredState: "present", ObservedState: client.VolumeActive, CreatedAt: stamp(), UpdatedAt: stamp(),
	}
}

func (f *fakeAPI) find(id string) *client.Volume {
	for i := range f.volumes {
		if f.volumes[i].ID == id {
			return &f.volumes[i]
		}
	}
	return nil
}

func (f *fakeAPI) CreateVolume(_ context.Context, req client.CreateVolumeRequest) (*client.OperationReference, error) {
	f.creates++
	f.lastCreate = req
	if f.createErr != nil && !f.createLands {
		return nil, f.createErr
	}
	f.nextID++
	id := fmt.Sprintf("vol_%d", f.nextID)
	size := req.SizeGB
	for _, o := range f.offerings {
		if o.Slug == req.DiskOfferingSlug && o.SizeGB != nil {
			size = *o.SizeGB
		}
	}
	storage := "shared"
	for _, o := range f.offerings {
		if o.Slug == req.DiskOfferingSlug {
			storage = o.StorageType
		}
	}
	v := volume(id, req.Name, req.DiskOfferingSlug, size, storage)
	if req.MountPoint != "" {
		v.MountPoint = ptr(req.MountPoint)
	}
	f.volumes = append(f.volumes, v)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &client.OperationReference{OperationID: "op_create", ResourceID: id, Status: client.OperationSubmitting}, nil
}

func (f *fakeAPI) GetVolume(_ context.Context, id string) (*client.Volume, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if v := f.find(id); v != nil {
		cp := *v
		return &cp, nil
	}
	return nil, fmt.Errorf("getting volume: %w", client.ErrNotFound)
}

// ListVolumes leaves out deleted volumes, as the real list does.
func (f *fakeAPI) ListVolumes(context.Context) ([]client.Volume, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var live []client.Volume
	for _, v := range f.volumes {
		if v.ObservedState != client.VolumeDeleted {
			live = append(live, v)
		}
	}
	return live, nil
}

func (f *fakeAPI) ListDiskOfferings(context.Context) ([]client.DiskOffering, error) {
	if f.offeringsErr != nil {
		return nil, f.offeringsErr
	}
	return f.offerings, nil
}

func (f *fakeAPI) ResizeVolume(_ context.Context, id, offering string, sizeGB int64) (*client.OperationReference, error) {
	f.resizes++
	v := f.find(id)
	var target *client.DiskOffering
	for i := range f.offerings {
		if f.offerings[i].Slug == offering {
			target = &f.offerings[i]
		}
	}
	if v == nil || target == nil {
		return nil, &client.APIError{Status: 422, Code: "VALIDATION_FAILED"}
	}
	size := sizeGB
	if target.SizeGB != nil {
		size = *target.SizeGB
	}
	if v.AttachedInstanceID != nil || size <= v.SizeGB {
		f.refused++
		return nil, &client.APIError{Status: 409, Code: client.CodeInvalidResourceState}
	}
	v.DiskOfferingSlug, v.SizeGB = offering, size
	return &client.OperationReference{OperationID: "op_resize", ResourceID: id}, nil
}

func (f *fakeAPI) AttachVolume(_ context.Context, id, instanceID string) (*client.OperationReference, error) {
	f.attaches++
	v := f.find(id)
	if v == nil {
		return nil, fmt.Errorf("attach: %w", client.ErrNotFound)
	}
	v.DesiredInstanceID = ptr(instanceID)
	if v.StorageType != nil && *v.StorageType == "shared" {
		f.pendingOpErr = &client.OperationError{Operation: client.Operation{ID: "op_attach", Kind: "attach_volume", Status: "failed",
			Failure: &client.OperationFailure{Code: "PROVISIONING_JOB_FAILED", Reason: "the provider reported job failure"}}}
	} else {
		v.AttachedInstanceID = ptr(instanceID)
	}
	return &client.OperationReference{OperationID: "op_attach", ResourceID: id}, nil
}

func (f *fakeAPI) DetachVolume(_ context.Context, id string) (*client.OperationReference, error) {
	f.detaches++
	v := f.find(id)
	if v == nil {
		return nil, fmt.Errorf("detach: %w", client.ErrNotFound)
	}
	if v.AttachedInstanceID == nil {
		f.redundantDetaches++
		v.DesiredInstanceID = nil // harmless, and it clears the stale intent
		f.pendingOpErr = &client.OperationError{Operation: client.Operation{ID: "op_detach", Kind: "detach_volume", Status: "failed",
			Failure: &client.OperationFailure{Code: "PROVISIONING_JOB_FAILED", Reason: "the provider reported job failure"}}}
	} else {
		v.AttachedInstanceID, v.DesiredInstanceID = nil, nil
	}
	return &client.OperationReference{OperationID: "op_detach", ResourceID: id}, nil
}

func (f *fakeAPI) DeleteVolume(_ context.Context, id string) (*client.OperationReference, error) {
	f.deletes++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	v := f.find(id)
	if v == nil {
		return nil, fmt.Errorf("delete: %w", client.ErrNotFound)
	}
	if v.AttachedInstanceID != nil || v.DesiredInstanceID != nil {
		f.refused++
		return nil, &client.APIError{Status: 409, Code: client.CodeInvalidResourceState}
	}
	v.ObservedState, v.DesiredState = client.VolumeDeleted, "deleted"
	return &client.OperationReference{OperationID: "op_delete", ResourceID: id}, nil
}

func (f *fakeAPI) WaitForOperation(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.pendingOpErr != nil {
		err := f.pendingOpErr
		f.pendingOpErr = nil
		return err
	}
	return f.settle(ctx, done)
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	if f.untilErr != nil {
		return f.untilErr
	}
	return f.settle(ctx, done)
}

// settle runs the done check once. It fails if the check does not pass and the
// operation is stuck, like a wait that can never finish.
func (f *fakeAPI) settle(ctx context.Context, done client.DoneCheck) error {
	if done != nil {
		reached, err := done(ctx)
		if err != nil {
			return err
		}
		if reached {
			return nil
		}
	}
	if f.opStuck {
		return errors.New("never finished and the done check did not pass")
	}
	return nil
}

// seeded returns a fake holding the given volumes and the default offerings.
func seeded(volumes ...client.Volume) *fakeAPI {
	return &fakeAPI{volumes: volumes, offerings: defaultOfferings(), nextID: len(volumes)}
}
