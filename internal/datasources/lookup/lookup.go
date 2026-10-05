// Package lookup holds what the single-item catalog data sources share.
package lookup

import (
	"fmt"
	"slices"
	"strings"
)

// NotFound words the error for a lookup that matched nothing. It names what was
// asked for and everything that exists, sorted, so a typo is obvious at a glance.
// scope qualifies the lookup (for example "for the vpc placement"), or is empty.
func NotFound(kind, attr, value, scope string, available []string) string {
	sorted := slices.Clone(available)
	slices.Sort(sorted)
	list := "none"
	if len(sorted) > 0 {
		list = strings.Join(sorted, ", ")
	}
	if scope != "" {
		scope = " " + scope
	}
	return fmt.Sprintf("No %s with %s %q%s. Available %ss: %s.", kind, attr, value, scope, attr, list)
}
