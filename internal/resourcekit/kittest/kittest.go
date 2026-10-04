// Package kittest builds the plan and state values Terraform would hand a
// resource, so unit tests can drive Create, Read and Delete without a Terraform
// binary. It is for tests only.
package kittest

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Ctx is the context tests pass to resource methods.
var Ctx = context.Background()

// SchemaOf returns the schema of a resource.
func SchemaOf(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(Ctx, resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func objectType(s schema.Schema) tftypes.Object {
	obj, ok := s.Type().TerraformType(Ctx).(tftypes.Object)
	if !ok {
		panic("the schema type is not an object")
	}
	return obj
}

// Str is a known string value.
func Str(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

// Num is a known number value.
func Num(v int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }

// Unknown is an unknown value of the given type, as a computed attribute is in a plan.
func Unknown(t tftypes.Type) tftypes.Value { return tftypes.NewValue(t, tftypes.UnknownValue) }

// UnknownStr is an unknown string.
func UnknownStr() tftypes.Value { return Unknown(tftypes.String) }

// Object returns an object value with every attribute null except those set.
func Object(s schema.Schema, set map[string]tftypes.Value) tftypes.Value {
	typ := objectType(s)
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tftypes.NewValue(typ, vals)
}

// Plan wraps attribute values as the plan Terraform passes to Create or Update.
func Plan(s schema.Schema, set map[string]tftypes.Value) tfsdk.Plan {
	return tfsdk.Plan{Schema: s, Raw: Object(s, set)}
}

// State wraps attribute values as stored state.
func State(s schema.Schema, set map[string]tftypes.Value) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: Object(s, set)}
}

// EmptyState is the state before Create: no resource.
func EmptyState(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(objectType(s), nil)}
}

// IsRemoved reports whether the state holds no resource.
func IsRemoved(st tfsdk.State) bool { return st.Raw.IsNull() }

// ErrorText joins the error diagnostics into one string for substring checks.
func ErrorText(d diag.Diagnostics) string {
	var b strings.Builder
	for _, e := range d.Errors() {
		b.WriteString(e.Summary() + " " + e.Detail() + "\n")
	}
	return b.String()
}

// Wait is the body every fake WaitForOperation shares: it succeeds as soon as the
// done check passes. With stuck set, the operation never finishes by itself, as a
// delete operation did on dev, so only the done check can end the wait.
func Wait(ctx context.Context, done func(context.Context) (bool, error), stuck bool) error {
	if done != nil {
		reached, err := done(ctx)
		if err != nil {
			return err
		}
		if reached {
			return nil
		}
	}
	if stuck {
		return errStuck
	}
	return nil
}

var errStuck = errorString("operation never finished and the done check did not pass")

type errorString string

func (e errorString) Error() string { return string(e) }
