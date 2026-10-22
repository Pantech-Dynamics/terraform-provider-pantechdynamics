package webhookeventtypes

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var ctx = context.Background()

type fakeAPI struct {
	list []client.WebhookEventType
	err  error
}

func (f *fakeAPI) ListWebhookEventTypes(context.Context) ([]client.WebhookEventType, error) {
	return f.list, f.err
}

func read(t *testing.T, api eventTypeAPI) datasource.ReadResponse {
	t.Helper()
	var sresp datasource.SchemaResponse
	New().Schema(ctx, datasource.SchemaRequest{}, &sresp)
	if d := sresp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
	typ, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	cfg := tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, vals)}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&DataSource{api: api}).Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	return resp
}

func TestReadListsEventTypesInOrder(t *testing.T) {
	api := &fakeAPI{list: []client.WebhookEventType{
		{Type: "instance.created", Resource: "instance", Description: "An instance finished creating."},
		{Type: "invoice.paid", Resource: "invoice", Description: "An invoice was paid."},
	}}
	resp := read(t, api)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if m.ID.ValueString() != "webhook_event_types" || len(m.EventTypes) != 2 || m.EventTypes[0].Type.ValueString() != "instance.created" ||
		m.EventTypes[1].Resource.ValueString() != "invoice" || m.EventTypes[1].Description.ValueString() != "An invoice was paid." {
		t.Errorf("model = %+v", m)
	}
}

func TestReadEmptyCatalogIsAnEmptyList(t *testing.T) {
	resp := read(t, &fakeAPI{})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m model
	resp.Diagnostics.Append(resp.State.Get(ctx, &m)...)
	if m.EventTypes == nil || len(m.EventTypes) != 0 {
		t.Errorf("event_types = %v, want an empty, non-null list", m.EventTypes)
	}
}

func TestReadUnavailableExplainsIt(t *testing.T) {
	resp := read(t, &fakeAPI{err: &client.APIError{Status: 503, Code: client.CodeWebhooksUnavailable}})
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not available on this platform yet") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}
