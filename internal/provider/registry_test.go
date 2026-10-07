package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// Every registered resource and data source must have a valid schema, a unique
// type name, a description on every attribute, and accept the provider's client.
func TestRegisteredSchemasAreValid(t *testing.T) {
	ctx := context.Background()
	p := &PantechDynamicsProvider{version: "test"}
	c, err := client.New("https://api.example.com/public/v1", "PAN_x", "test")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}

	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &meta)
		if seen["resource "+meta.TypeName] {
			t.Errorf("duplicate resource %s", meta.TypeName)
		}
		seen["resource "+meta.TypeName] = true

		var sresp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sresp)
		if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s: %v", meta.TypeName, d)
		}
		for name, a := range sresp.Schema.Attributes {
			if a.GetDescription() == "" && name != "timeouts" { // the timeouts block is the framework's
				t.Errorf("%s.%s has no description", meta.TypeName, name)
			}
		}
		if rc, ok := r.(resource.ResourceWithConfigure); ok {
			var cresp resource.ConfigureResponse
			rc.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &cresp)
			if cresp.Diagnostics.HasError() {
				t.Errorf("%s: %v", meta.TypeName, cresp.Diagnostics)
			}
		}
	}

	for _, newDataSource := range p.DataSources(ctx) {
		d := newDataSource()
		var meta datasource.MetadataResponse
		d.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &meta)
		if seen["data "+meta.TypeName] {
			t.Errorf("duplicate data source %s", meta.TypeName)
		}
		seen["data "+meta.TypeName] = true

		var sresp datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &sresp)
		if diags := sresp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%s: %v", meta.TypeName, diags)
		}
		if dc, ok := d.(datasource.DataSourceWithConfigure); ok {
			var cresp datasource.ConfigureResponse
			dc.Configure(ctx, datasource.ConfigureRequest{ProviderData: c}, &cresp)
			if cresp.Diagnostics.HasError() {
				t.Errorf("%s: %v", meta.TypeName, cresp.Diagnostics)
			}
		}
	}

	for _, want := range []string{"resource pantechdynamics_database", "resource pantechdynamics_database_snapshot", "data pantechdynamics_database_engines", "data pantechdynamics_security_group", "data pantechdynamics_ssh_key", "data pantechdynamics_instance", "data pantechdynamics_network", "resource pantechdynamics_load_balancer", "data pantechdynamics_load_balancer", "resource pantechdynamics_kubernetes_cluster", "data pantechdynamics_kubernetes_cluster", "data pantechdynamics_kubernetes_versions"} {
		if !seen[want] {
			t.Errorf("%s is not registered", want)
		}
	}
}
