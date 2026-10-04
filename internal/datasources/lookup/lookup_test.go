package lookup_test

import (
	"testing"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
)

func TestNotFound(t *testing.T) {
	avail := []string{"starter", "individual"}
	tests := []struct {
		name, scope string
		avail       []string
		want        string
	}{
		{"sorted and scoped", "for the vpc placement", avail, `No plan with slug "nope" for the vpc placement. Available slugs: individual, starter.`},
		{"no scope", "", avail, `No plan with slug "nope". Available slugs: individual, starter.`},
		{"nothing exists", "", nil, `No plan with slug "nope". Available slugs: none.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lookup.NotFound("plan", "slug", "nope", tt.scope, tt.avail); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
	if avail[0] != "starter" {
		t.Error("NotFound must not reorder the caller's slice")
	}
}
