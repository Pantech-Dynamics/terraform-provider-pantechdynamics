package kubernetescluster

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// control_nodes is immutable and Optional+Computed, and the backend reports it,
// so Read stores the real count in state. A static Default is re-applied over
// that value on any plan that omits control_nodes (an import, or a value set
// once and then removed), and a plain RequiresReplace then destroys the cluster,
// with its nodes, disks and persistent volumes, to reconcile a difference only
// the default created. The attribute must carry no static default: the backend
// assigns 1 when it is omitted (control_nodes is sent omitempty), the value is
// held across plans by UseStateForUnknown, and it is replaced only when the user
// configures a different value, by RequiresReplaceIfConfigured.
func TestControlNodesHasNoStaticDefault(t *testing.T) {
	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New did not return a *Resource")
	}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["control_nodes"].(schema.Int64Attribute)
	if !ok {
		t.Fatal("control_nodes is missing or is not an Int64Attribute")
	}
	if !attr.Optional || !attr.Computed {
		t.Fatalf("control_nodes must stay Optional and Computed, got Optional=%v Computed=%v", attr.Optional, attr.Computed)
	}
	if attr.Default != nil {
		t.Fatal("control_nodes must not declare a static Default: it is re-applied over imported or omitted state and forces the cluster to be destroyed and recreated; use UseStateForUnknown and RequiresReplaceIfConfigured and let the backend default stand")
	}
}
