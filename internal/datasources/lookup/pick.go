package lookup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Paths of the two selector attributes every id-or-name lookup has.
var (
	PathID   = path.Root("id")
	PathName = path.Root("name")
)

// ValidateIDOrName requires exactly one of the id and name attributes. An
// unknown value counts as set, since it will be known at apply.
func ValidateIDOrName(ctx context.Context, cfg tfsdk.Config, kind string, diags *diag.Diagnostics) {
	var id, name types.String
	diags.Append(cfg.GetAttribute(ctx, PathID, &id)...)
	diags.Append(cfg.GetAttribute(ctx, PathName, &name)...)
	if diags.HasError() {
		return
	}
	if id.IsNull() == name.IsNull() {
		diags.AddError("Set exactly one of id and name",
			fmt.Sprintf("Look a %s up either by id or by name, not both and not neither.", kind))
	}
}

// Pick returns the one item whose id equals id, or, when id is empty, whose name
// equals name. Names are not unique for every kind of resource, so a name that
// matches more than one item is an error that lists their ids. kind names the
// resource in errors, for example "network".
func Pick[T any](items []T, kind, id, name string, idOf, nameOf func(T) string) (T, error) {
	var zero T
	attr, want, key := "id", id, idOf
	if id == "" {
		attr, want, key = "name", name, nameOf
	}

	var matches []T
	available := make([]string, 0, len(items))
	for _, it := range items {
		available = append(available, key(it))
		if key(it) == want {
			matches = append(matches, it)
		}
	}
	switch len(matches) {
	case 0:
		return zero, errors.New(NotFound(kind, attr, want, "", available))
	case 1:
		return matches[0], nil
	}
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, idOf(m))
	}
	return zero, fmt.Errorf("%d %ss are named %q (%s). Look it up by id instead", len(matches), kind, want, strings.Join(ids, ", "))
}
