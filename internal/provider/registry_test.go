package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
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

	for _, newEphemeral := range p.EphemeralResources(ctx) {
		e := newEphemeral()
		var meta ephemeral.MetadataResponse
		e.Metadata(ctx, ephemeral.MetadataRequest{ProviderTypeName: "pantechdynamics"}, &meta)
		seen["ephemeral "+meta.TypeName] = true
		var sresp ephemeral.SchemaResponse
		e.Schema(ctx, ephemeral.SchemaRequest{}, &sresp)
		if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s: %v", meta.TypeName, d)
		}
		if ec, ok := e.(ephemeral.EphemeralResourceWithConfigure); ok {
			var cresp ephemeral.ConfigureResponse
			ec.Configure(ctx, ephemeral.ConfigureRequest{ProviderData: c}, &cresp)
			if cresp.Diagnostics.HasError() {
				t.Errorf("%s: %v", meta.TypeName, cresp.Diagnostics)
			}
		}
	}

	for _, want := range []string{"resource pantechdynamics_database", "resource pantechdynamics_database_snapshot", "data pantechdynamics_database_engines", "data pantechdynamics_security_group", "data pantechdynamics_ssh_key", "data pantechdynamics_instance", "data pantechdynamics_network", "resource pantechdynamics_load_balancer", "data pantechdynamics_load_balancer", "resource pantechdynamics_kubernetes_cluster", "data pantechdynamics_kubernetes_cluster", "data pantechdynamics_kubernetes_versions", "resource pantechdynamics_webhook", "data pantechdynamics_webhook_event_types", "resource pantechdynamics_dns_zone", "resource pantechdynamics_dns_record", "resource pantechdynamics_domain", "data pantechdynamics_dns_zone", "data pantechdynamics_dns_records", "data pantechdynamics_domain", "data pantechdynamics_domain_tlds", "resource pantechdynamics_container_registry_namespace", "resource pantechdynamics_container_repository", "resource pantechdynamics_container_registry_token", "data pantechdynamics_container_repository", "data pantechdynamics_container_repositories", "resource pantechdynamics_team", "resource pantechdynamics_team_membership", "resource pantechdynamics_access_policy", "resource pantechdynamics_access_policy_attachment", "resource pantechdynamics_access_settings", "data pantechdynamics_access_policy", "data pantechdynamics_iam_actions", "data pantechdynamics_access_policy_document", "resource pantechdynamics_certificate", "resource pantechdynamics_certificate_import", "data pantechdynamics_certificate", "data pantechdynamics_certificate_export", "ephemeral pantechdynamics_certificate_export"} {
		if !seen[want] {
			t.Errorf("%s is not registered", want)
		}
	}
}
