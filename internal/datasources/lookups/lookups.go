// Package lookups holds the data sources that find one existing account
// resource by id or by name: pantechdynamics_security_group, _ssh_key,
// _instance, _network, _load_balancer and _kubernetes_cluster. They share the
// selector attributes and the matching rules in package lookup.
package lookups

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
)

// selectorAttributes are the id and name attributes every lookup has: set
// exactly one, and the other is filled in.
func selectorAttributes(kind, idPrefix string, namesUnique bool) map[string]schema.Attribute {
	nameNote := "Names are unique in the account."
	if !namesUnique {
		nameNote = "Names are not unique on the platform: if more than one " + kind + " has the name, the lookup fails and lists their ids."
	}
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Description: fmt.Sprintf("Id of the %s to look up, starting with %s. Set exactly one of id and name.", kind, idPrefix),
			Optional:    true,
			Computed:    true,
		},
		"name": schema.StringAttribute{
			Description: fmt.Sprintf("Name of the %s to look up. It must match exactly. %s Set exactly one of id and name.", kind, nameNote),
			Optional:    true,
			Computed:    true,
		},
	}
}

// pathID is where the id selector lives.
var pathID = lookup.PathID

// selector reads the id or name the configuration asked for.
func selector(id, name types.String) (string, string) {
	return id.ValueString(), name.ValueString()
}

// configureAPI type-checks the provider data against the API a data source
// needs. T is that data source's API interface.
func configureAPI[T any](req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) (T, bool) {
	var zero T
	if req.ProviderData == nil {
		return zero, false
	}
	api, ok := req.ProviderData.(T)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return zero, false
	}
	return api, true
}

// validateSelector is the ValidateConfig body every lookup shares.
func validateSelector(ctx context.Context, req datasource.ValidateConfigRequest, kind string, diags *diag.Diagnostics) {
	lookup.ValidateIDOrName(ctx, req.Config, kind, diags)
}
