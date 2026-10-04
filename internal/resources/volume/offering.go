package volume

import (
	"fmt"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// offeringProblem is a mistake in the offering and size a user chose. Field names
// the attribute to point at.
type offeringProblem struct {
	Field   string
	Message string
}

// checkOffering validates the offering and size before anything is ordered. The
// API silently ignores size_gb on a fixed offering and makes the offering's own
// size, so a mismatch is caught here, not discovered after the volume exists. It
// returns the offering, or the problem.
func checkOffering(offerings []client.DiskOffering, slug string, sizeGB int64, sizeSet bool) (*client.DiskOffering, *offeringProblem) {
	var found *client.DiskOffering
	for i := range offerings {
		if offerings[i].Slug == slug {
			found = &offerings[i]
		}
	}
	switch {
	case found == nil:
		return nil, &offeringProblem{"disk_offering_slug", fmt.Sprintf("There is no disk offering %q. The pantechdynamics_disk_offerings data source lists the valid slugs.", slug)}
	case found.CustomSize && !sizeSet:
		return nil, &offeringProblem{"size_gb", fmt.Sprintf("size_gb is required for the customized offering %q.", slug)}
	case !found.CustomSize && sizeSet && found.SizeGB != nil && *found.SizeGB != sizeGB:
		return nil, &offeringProblem{"size_gb", fmt.Sprintf("The offering %q has a fixed size of %d GB, and the API would ignore size_gb = %d and create %d GB. Remove size_gb, or choose a customized offering.", slug, *found.SizeGB, sizeGB, *found.SizeGB)}
	}
	return found, nil
}

// targetSize is the size a volume will have on this offering: the offering's own
// for a fixed one, the ordered size for a customized one.
func targetSize(o *client.DiskOffering, sizeGB int64) int64 {
	if o.CustomSize || o.SizeGB == nil {
		return sizeGB
	}
	return *o.SizeGB
}
