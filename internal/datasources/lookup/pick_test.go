package lookup_test

import (
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
)

type item struct{ id, name string }

func TestPick(t *testing.T) {
	items := []item{{"net_1", "main"}, {"net_2", "dup"}, {"net_3", "dup"}}
	idOf := func(i item) string { return i.id }
	nameOf := func(i item) string { return i.name }
	tests := []struct {
		name, id, byName string
		wantID           string
		wantErr          string
	}{
		{"by id", "net_2", "", "net_2", ""},
		{"by name", "", "main", "net_1", ""},
		{"unknown id lists ids", "net_9", "", "", "Available ids: net_1, net_2, net_3."},
		{"unknown name lists names", "", "nope", "", `No network with name "nope". Available names: dup, main.`},
		{"ambiguous name", "", "dup", "", `2 networks are named "dup" (net_2, net_3)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lookup.Pick(items, "network", tt.id, tt.byName, idOf, nameOf)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got.id != tt.wantID {
				t.Fatalf("got %+v, err %v", got, err)
			}
		})
	}
}
