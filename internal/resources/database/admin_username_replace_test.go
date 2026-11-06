package database

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// admin_username is immutable and Optional+Computed, and the backend reports it,
// so Read stores the real login in state. A static Default is re-applied over
// that value on any plan that omits admin_username (an import, or a value set
// once and then removed), and a plain RequiresReplace then destroys the database
// and all of its data to reconcile a difference only the default created. The
// attribute must carry no static default: the backend assigns "dbadmin" when it
// is omitted (admin_username is sent omitempty), the value is held across plans
// by UseStateForUnknown, and it is replaced only when the user configures a
// different value, by RequiresReplaceIfConfigured.
func TestAdminUsernameHasNoStaticDefault(t *testing.T) {
	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New did not return a *Resource")
	}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["admin_username"].(schema.StringAttribute)
	if !ok {
		t.Fatal("admin_username is missing or is not a StringAttribute")
	}
	if !attr.Optional || !attr.Computed {
		t.Fatalf("admin_username must stay Optional and Computed, got Optional=%v Computed=%v", attr.Optional, attr.Computed)
	}
	if attr.Default != nil {
		t.Fatal("admin_username must not declare a static Default: it is re-applied over imported or omitted state and forces the database, with all its data, to be destroyed and recreated; use UseStateForUnknown and RequiresReplaceIfConfigured and let the backend default stand")
	}
}
