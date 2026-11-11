package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	. "github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit/kittest"
)

// fakeAPI is an in-memory domainAPI. A registration answers registering with
// an order that ends orderStatus; the domain then becomes active with the
// backend's defaults (transfer lock on, Pantech DNS).
type fakeAPI struct {
	domains  map[string]client.Domain
	contacts map[string]client.DomainContact

	orderStatus, orderFailure  string
	orders                     []client.DomainOrder
	registerErr, nameServerErr error
	contactErr                 error

	registers, patches, nameServerSets, contactSets int
	lastRegister                                    client.RegisterDomainRequest
	lastPatch                                       client.UpdateDomainRequest
	lastNameServers                                 client.SetDomainNameServersRequest
	lastContact                                     client.DomainContact
}

func newFake() *fakeAPI {
	return &fakeAPI{domains: map[string]client.Domain{}, contacts: map[string]client.DomainContact{}, orderStatus: client.DomainOrderSucceeded}
}

func ptr(s string) *string { return &s }

var pantechNS = []string{"ns-1.example-dns.org", "ns-2.example-dns.com"}

func (f *fakeAPI) RegisterDomain(_ context.Context, req client.RegisterDomainRequest) (*client.DomainOrderResult, error) {
	f.registers++
	f.lastRegister = req
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	d := client.Domain{ID: "dom_1", Name: req.Name, TLD: "com", Status: "registering", AutoRenew: *req.AutoRenew, Privacy: *req.Privacy, TransferLock: true,
		NameServerMode: client.NameServerModePantech, NameServers: pantechNS}
	f.domains[d.ID] = d
	f.contacts[d.ID] = req.Contact
	return &client.DomainOrderResult{Domain: d, Order: client.DomainOrder{ID: "dor_1", DomainID: d.ID, DomainName: d.Name, Kind: "register", Status: "pending"}}, nil
}

func (f *fakeAPI) GetDomain(_ context.Context, id string) (*client.Domain, error) {
	d, ok := f.domains[id]
	if !ok {
		return nil, fmt.Errorf("getting domain: %w", &client.APIError{Status: 404, Code: client.CodeDomainNotFound})
	}
	if d.Status == "registering" {
		d.Status = client.DomainActive
	}
	d.SettingsPending = false
	f.domains[id] = d
	return &d, nil
}

func (f *fakeAPI) ListDomains(context.Context, string) ([]client.Domain, error) {
	var out []client.Domain
	for _, d := range f.domains {
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeAPI) UpdateDomain(_ context.Context, id string, req client.UpdateDomainRequest) (*client.Domain, error) {
	f.patches++
	f.lastPatch = req
	d := f.domains[id]
	if req.AutoRenew != nil {
		d.AutoRenew = *req.AutoRenew
	}
	if req.TransferLock != nil {
		d.TransferLock = *req.TransferLock
	}
	if req.Privacy != nil {
		d.Privacy = *req.Privacy
	}
	d.SettingsPending = true
	f.domains[id] = d
	return &d, nil
}

func (f *fakeAPI) SetDomainNameServers(_ context.Context, id string, req client.SetDomainNameServersRequest) (*client.Domain, error) {
	f.nameServerSets++
	f.lastNameServers = req
	if f.nameServerErr != nil {
		return nil, f.nameServerErr
	}
	d := f.domains[id]
	if req.UsePantechDNS {
		d.NameServerMode, d.NameServers = client.NameServerModePantech, pantechNS
	} else {
		d.NameServerMode = client.NameServerModeCustom
		d.NameServers = nil
		for _, ns := range req.NameServers {
			d.NameServers = append(d.NameServers, strings.ToLower(strings.TrimSuffix(ns, ".")))
		}
	}
	d.SettingsPending = true
	f.domains[id] = d
	return &d, nil
}

func (f *fakeAPI) GetDomainContact(_ context.Context, id string) (*client.DomainContact, error) {
	if f.contactErr != nil {
		return nil, f.contactErr
	}
	c := f.contacts[id]
	return &c, nil
}

func (f *fakeAPI) UpdateDomainContact(_ context.Context, id string, contact client.DomainContact) (*client.Domain, error) {
	f.contactSets++
	f.lastContact = contact
	f.contacts[id] = contact
	d := f.domains[id]
	return &d, nil
}

func (f *fakeAPI) WaitForDomainOrder(_ context.Context, id string) (*client.DomainOrder, error) {
	o := client.DomainOrder{ID: id, DomainID: "dom_1", DomainName: "example.com", Kind: "register", Status: f.orderStatus}
	if f.orderStatus == client.DomainOrderAwaitingPayment {
		o.PaymentURL = ptr("https://pay.example/o/" + id)
		return nil, &client.DomainPaymentRequiredError{Order: o}
	}
	if f.orderStatus == client.DomainOrderFailed {
		o.FailureCode, o.Refunded = ptr(f.orderFailure), true
		d := f.domains["dom_1"]
		d.Status = client.DomainFailed
		f.domains["dom_1"] = d
		return nil, &client.DomainOrderError{Order: o}
	}
	return &o, nil
}

func (f *fakeAPI) ListDomainOrders(context.Context, string) ([]client.DomainOrder, error) {
	return f.orders, nil
}

func (f *fakeAPI) ListDomainTLDs(context.Context) ([]client.TLDOffer, error) {
	return []client.TLDOffer{{TLD: "com", Currency: "USD", RegisterMinor: 1500, RenewMinor: 1600}, {TLD: "uk", Currency: "USD", RegisterMinor: 900, RenewMinor: 900}, {TLD: "co.uk", Currency: "USD", RegisterMinor: 1000, RenewMinor: 1000}}, nil
}

func (f *fakeAPI) WaitUntil(ctx context.Context, _ string, done client.DoneCheck) error {
	for range 3 {
		ok, err := done(ctx)
		if err != nil || ok {
			return err
		}
	}
	return context.DeadlineExceeded
}

func testSchema(t *testing.T) schema.Schema { return SchemaOf(t, New()) }

func contactType(s schema.Schema) tftypes.Object {
	typ, ok := s.Attributes["contact"].GetType().TerraformType(Ctx).(tftypes.Object)
	if !ok {
		panic("contact is not an object")
	}
	return typ
}

func contactVal(s schema.Schema, email string) tftypes.Value {
	return tftypes.NewValue(contactType(s), map[string]tftypes.Value{
		"type": Str("person"), "first_name": Str("Ada"), "last_name": Str("Obi"), "organization_name": Str(""), "email": Str(email),
		"phone": Str("+234.8012345678"), "address_line1": Str("1 Marina"), "address_line2": Str(""), "city": Str("Lagos"), "state": Str(""),
		"country_code": Str("NG"), "postal_code": Str(""),
	})
}

func boolVal(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }

func nsSet(vals ...string) tftypes.Value {
	typ := tftypes.Set{ElementType: tftypes.String}
	if vals == nil {
		return tftypes.NewValue(typ, nil)
	}
	elems := make([]tftypes.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, Str(v))
	}
	return tftypes.NewValue(typ, elems)
}

// attrs are the arguments of example.com with every computed attribute set as
// given (unknown in a create plan, known in state).
func attrs(s schema.Schema, transferLock bool, ns tftypes.Value, computed func(string) tftypes.Value) map[string]tftypes.Value {
	m := map[string]tftypes.Value{
		"name": Str("example.com"), "years": Num(2), "contact": contactVal(s, "ada@example.com"), "auto_renew": boolVal(true),
		"transfer_lock": boolVal(transferLock), "privacy": boolVal(true), "name_servers": ns, "accept_terms": boolVal(true),
	}
	for _, name := range []string{"id", "tld", "status", "name_server_mode", "zone_id", "order_id", "registered_at", "expires_at"} {
		m[name] = computed(name)
	}
	m["current_name_servers"] = Unknown(tftypes.List{ElementType: tftypes.String})
	m["contact_verified"] = Unknown(tftypes.Bool)
	return m
}

func planFor(s schema.Schema, transferLock bool, ns tftypes.Value) tfsdk.Plan {
	return Plan(s, attrs(s, transferLock, ns, func(string) tftypes.Value { return UnknownStr() }))
}

func getModel(t *testing.T, st tfsdk.State) model {
	t.Helper()
	var m model
	if d := st.Get(Ctx, &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

func create(t *testing.T, api *fakeAPI, plan tfsdk.Plan) *resource.CreateResponse {
	t.Helper()
	resp := &resource.CreateResponse{State: EmptyState(testSchema(t))}
	(&Resource{api: api}).Create(Ctx, resource.CreateRequest{Plan: plan}, resp)
	return resp
}

func TestCreateRegistersThenAppliesTheSettings(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	resp := create(t, api, planFor(s, false, nsSet("NS1.example.net.", "ns2.example.net")))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if r := api.lastRegister; r.Name != "example.com" || r.Years != 2 || !r.AcceptTerms || r.Contact.Email != "ada@example.com" || !*r.AutoRenew {
		t.Fatalf("register sent %+v", r)
	}
	if api.patches != 1 || api.lastPatch.TransferLock == nil || *api.lastPatch.TransferLock || api.lastPatch.AutoRenew != nil {
		t.Fatalf("patch = %+v (%d calls), want only transfer_lock = false", api.lastPatch, api.patches)
	}
	if api.nameServerSets != 1 || api.lastNameServers.UsePantechDNS || len(api.lastNameServers.NameServers) != 2 {
		t.Fatalf("name servers sent %+v", api.lastNameServers)
	}
	m := getModel(t, resp.State)
	var ns []string
	m.NameServers.ElementsAs(Ctx, &ns, false)
	if m.ID.ValueString() != "dom_1" || m.OrderID.ValueString() != "dor_1" || m.Status.ValueString() != "active" || m.TransferLock.ValueBool() || m.NameServerMode.ValueString() != "custom" {
		t.Fatalf("model = %+v", m)
	}
	if len(ns) != 2 || !strings.Contains(strings.Join(ns, ","), "NS1.example.net.") {
		t.Fatalf("name_servers = %v, want the configured spelling", ns)
	}
	if m.Years.ValueInt64() != 2 || m.Contact == nil || m.Contact.Email.ValueString() != "ada@example.com" || !m.AcceptTerms.ValueBool() {
		t.Fatalf("carried-over attributes = %+v", m)
	}
}

// With name_servers unset and the backend defaults matching, nothing is sent
// after the order.
func TestCreateWithPantechDNSSendsNoChanges(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	resp := create(t, api, planFor(s, true, nsSet()))
	if resp.Diagnostics.HasError() || api.patches != 0 || api.nameServerSets != 0 {
		t.Fatalf("diagnostics = %v, patches = %d, name server sets = %d", resp.Diagnostics, api.patches, api.nameServerSets)
	}
	m := getModel(t, resp.State)
	if !m.NameServers.IsNull() || m.NameServerMode.ValueString() != "pantech" {
		t.Fatalf("model = %+v", m)
	}
}

// A failed order registered nothing (the amount is returned), so it is not
// kept in state: the next apply places a new order.
func TestCreateWithAFailedOrderLeavesNoStateAndExplains(t *testing.T) {
	api := newFake()
	api.orderStatus, api.orderFailure = client.DomainOrderFailed, "insufficient_credit"
	resp := create(t, api, planFor(testSchema(t), true, nsSet()))
	text := ErrorText(resp.Diagnostics)
	if !strings.Contains(text, "insufficient_credit") || !strings.Contains(text, "Add credit") || !strings.Contains(text, "returned") {
		t.Fatalf("diagnostics = %q", text)
	}
	if !IsRemoved(resp.State) {
		t.Fatal("a failed order must leave no state")
	}
}

// A create retried after an interrupted apply finds the domain already in the
// organization: it is adopted, and nothing is bought twice.
func TestCreateAdoptsADomainAlreadyInTheOrganization(t *testing.T) {
	api := newFake()
	api.domains["dom_9"] = client.Domain{ID: "dom_9", Name: "example.com", TLD: "com", Status: client.DomainActive, AutoRenew: true, TransferLock: true, Privacy: true, NameServerMode: client.NameServerModePantech}
	api.registerErr = &client.APIError{Status: 409, Code: client.CodeDomainAlreadyHeld}
	resp := create(t, api, planFor(testSchema(t), true, nsSet()))
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if m := getModel(t, resp.State); m.ID.ValueString() != "dom_9" || !m.OrderID.IsNull() {
		t.Fatalf("model = %+v", m)
	}
}

func TestCreateOfADomainHeldElsewhereFails(t *testing.T) {
	api := newFake()
	api.registerErr = &client.APIError{Status: 409, Code: client.CodeDomainAlreadyHeld}
	resp := create(t, api, planFor(testSchema(t), true, nsSet()))
	if !strings.Contains(ErrorText(resp.Diagnostics), "another organization") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

func TestCreateRefusalsAreExplained(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&client.APIError{Status: 409, Code: client.CodeDomainNotAvailable}, "Choose another name"},
		{&client.APIError{Status: 422, Code: client.CodeTLDNotSold}, "pantechdynamics_domain_tlds"},
		{&client.APIError{Status: 422, Code: "VALIDATION_FAILED", Errors: []client.FieldError{{Field: "contact.phone", Code: "INVALID", Message: "must be +CC.NUMBER"}}}, "must be +CC.NUMBER"},
	}
	for _, tt := range tests {
		api := newFake()
		api.registerErr = tt.err
		resp := create(t, api, planFor(testSchema(t), true, nsSet()))
		if !strings.Contains(ErrorText(resp.Diagnostics), tt.want) {
			t.Errorf("diagnostics = %q, want %q", ErrorText(resp.Diagnostics), tt.want)
		}
	}
	if p, ok := attributeFor("contact.phone"); !ok || !p.Equal(path.Root("contact").AtName("phone")) {
		t.Errorf("contact.phone maps to %v", p)
	}
}

// stateOf is the state Create leaves for dom_1, transfer lock on.
func stateOf(t *testing.T, api *fakeAPI, ns tftypes.Value) tfsdk.State {
	t.Helper()
	resp := create(t, api, planFor(testSchema(t), true, ns))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	return resp.State
}

func TestReadKeepsStateStableAndDetectsDrift(t *testing.T) {
	api := newFake()
	st := stateOf(t, api, nsSet("ns1.example.net", "ns2.example.net"))
	read := func() model {
		resp := &resource.ReadResponse{State: st}
		(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		return getModel(t, resp.State)
	}
	if m := read(); m.NameServers.IsNull() || m.Contact == nil {
		t.Fatalf("model = %+v", m)
	}
	// Pointed back at Pantech DNS outside Terraform: name_servers reads null,
	// so the next plan shows the custom servers being set again.
	d := api.domains["dom_1"]
	d.NameServerMode = client.NameServerModePantech
	api.domains["dom_1"] = d
	if m := read(); !m.NameServers.IsNull() {
		t.Fatalf("name_servers = %v, want null", m.NameServers)
	}
}

func TestReadAfterImportFetchesTheContact(t *testing.T) {
	api := newFake()
	api.domains["dom_1"] = client.Domain{ID: "dom_1", Name: "example.com", Status: client.DomainActive, NameServerMode: client.NameServerModePantech}
	api.contacts["dom_1"] = client.DomainContact{Type: "person", Email: "ada@example.com"}
	s := testSchema(t)
	imported := State(s, map[string]tftypes.Value{"id": Str("dom_1")})
	resp := &resource.ReadResponse{State: imported}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: imported}, resp)
	m := getModel(t, resp.State)
	if resp.Diagnostics.HasError() || m.Contact == nil || m.Contact.Email.ValueString() != "ada@example.com" || m.Name.ValueString() != "example.com" {
		t.Fatalf("diagnostics = %v, model = %+v", resp.Diagnostics, m)
	}

	api.contactErr = &client.APIError{Status: 403, Code: "FORBIDDEN"}
	resp = &resource.ReadResponse{State: imported}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: imported}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 || getModel(t, resp.State).Contact != nil {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestReadRemovesADomainNoLongerInTheOrganization(t *testing.T) {
	api := newFake()
	st := stateOf(t, api, nsSet())
	delete(api.domains, "dom_1")
	resp := &resource.ReadResponse{State: st}
	(&Resource{api: api}).Read(Ctx, resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !IsRemoved(resp.State) {
		t.Fatal(resp.Diagnostics)
	}
}

func TestUpdateSendsTheContactAndPointsBackAtPantechDNS(t *testing.T) {
	api := newFake()
	st := stateOf(t, api, nsSet("ns1.example.net", "ns2.example.net"))
	s := testSchema(t)
	vals := attrs(s, true, nsSet(), func(string) tftypes.Value { return UnknownStr() })
	vals["contact"] = contactVal(s, "new@example.com")
	resp := &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: Plan(s, vals), State: st}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if api.contactSets != 1 || api.lastContact.Email != "new@example.com" || !api.lastNameServers.UsePantechDNS {
		t.Fatalf("contact = %+v, name servers = %+v", api.lastContact, api.lastNameServers)
	}
	if m := getModel(t, resp.State); !m.NameServers.IsNull() || m.Contact.Email.ValueString() != "new@example.com" || m.OrderID.ValueString() != "dor_1" {
		t.Fatalf("model = %+v", m)
	}
}

func TestUpdateWithoutAZoneExplainsPantechDNS(t *testing.T) {
	api := newFake()
	st := stateOf(t, api, nsSet("ns1.example.net", "ns2.example.net"))
	api.nameServerErr = &client.APIError{Status: 404, Code: client.CodeDNSZoneNotFound}
	s := testSchema(t)
	resp := &resource.UpdateResponse{State: st}
	(&Resource{api: api}).Update(Ctx, resource.UpdateRequest{Plan: Plan(s, attrs(s, true, nsSet(), func(string) tftypes.Value { return UnknownStr() })), State: st}, resp)
	if !strings.Contains(ErrorText(resp.Diagnostics), "pantechdynamics_dns_zone") {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

// Destroy never releases the domain: no API call, and a warning says so.
func TestDeleteOnlyWarns(t *testing.T) {
	api := newFake()
	st := stateOf(t, api, nsSet())
	before := *api
	resp := &resource.DeleteResponse{State: st}
	(&Resource{api: api}).Delete(Ctx, resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 || !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "cannot be undone") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if api.registers != before.registers || api.patches != before.patches || api.nameServerSets != before.nameServerSets || len(api.domains) != 1 {
		t.Fatal("delete must not call the API")
	}
}

func TestModifyPlanWarnsWithThePrice(t *testing.T) {
	api := newFake()
	s := testSchema(t)
	plan := planFor(s, true, nsSet())
	resp := &resource.ModifyPlanResponse{Plan: plan}
	(&Resource{api: api}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: plan, State: EmptyState(s)}, resp)
	if resp.Diagnostics.WarningsCount() != 1 || !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "USD 31.00") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}

	st := stateOf(t, api, nsSet())
	resp = &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(Ctx), nil)}}
	(&Resource{api: api}).ModifyPlan(Ctx, resource.ModifyPlanRequest{Plan: resp.Plan, State: st}, resp)
	if resp.Diagnostics.WarningsCount() != 1 || resp.Diagnostics.Warnings()[0].Summary() != "Destroy does not release the domain" {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestOfferForPrefersTheLongestTLD(t *testing.T) {
	tlds, _ := newFake().ListDomainTLDs(Ctx)
	if o, ok := offerFor(tlds, "shop.co.uk"); !ok || o.TLD != "co.uk" {
		t.Fatalf("offer = %+v", o)
	}
	if _, ok := offerFor(tlds, "example.org"); ok {
		t.Fatal("org is not sold")
	}
	if got := formatMinor(123456789); got != "1,234,567.89" {
		t.Fatalf("formatMinor = %s", got)
	}
}

func TestValidators(t *testing.T) {
	var b validator.BoolResponse
	mustBeTrue{}.ValidateBool(Ctx, validator.BoolRequest{Path: path.Root("accept_terms"), ConfigValue: types.BoolValue(false)}, &b)
	if !b.Diagnostics.HasError() {
		t.Error("accept_terms = false must be refused")
	}
	for phone, ok := range map[string]bool{"+234.8012345678": true, "+1.5551234": true, "08012345678": false, "+234 801": false} {
		var r validator.StringResponse
		phoneFormat{}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("phone"), ConfigValue: types.StringValue(phone)}, &r)
		if r.Diagnostics.HasError() == ok {
			t.Errorf("phone %q: error = %v", phone, r.Diagnostics.HasError())
		}
		if strings.Contains(ErrorText(r.Diagnostics), phone) {
			t.Errorf("the phone number must not be echoed: %q", ErrorText(r.Diagnostics))
		}
	}
	for cc, ok := range map[string]bool{"NG": true, "ng": false, "NGA": false} {
		var r validator.StringResponse
		countryCode{}.ValidateString(Ctx, validator.StringRequest{Path: path.Root("country_code"), ConfigValue: types.StringValue(cc)}, &r)
		if r.Diagnostics.HasError() == ok {
			t.Errorf("country %q: error = %v", cc, r.Diagnostics.HasError())
		}
	}
	one, _ := types.SetValueFrom(Ctx, types.StringType, []string{"ns1.example.net"})
	bad, _ := types.SetValueFrom(Ctx, types.StringType, []string{"ns1.example.net", "localhost"})
	good, _ := types.SetValueFrom(Ctx, types.StringType, []string{"ns1.example.net", "ns2.example.net"})
	for name, tt := range map[string]struct {
		v       types.Set
		wantErr bool
	}{"one": {one, true}, "bad host": {bad, true}, "good": {good, false}} {
		var r validator.SetResponse
		nameServerList{}.ValidateSet(Ctx, validator.SetRequest{Path: path.Root("name_servers"), ConfigValue: tt.v}, &r)
		if r.Diagnostics.HasError() != tt.wantErr {
			t.Errorf("%s: error = %v", name, r.Diagnostics.HasError())
		}
	}
}

// Credit short: the order waits for a card payment. The apply stops with the
// link and keeps nothing in state.
func TestCreateStopsForPayment(t *testing.T) {
	api := newFake()
	api.orderStatus = client.DomainOrderAwaitingPayment
	resp := create(t, api, planFor(testSchema(t), true, nsSet()))
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || errs[0].Summary() != "Payment needed to register the domain" || !strings.Contains(errs[0].Detail(), "https://pay.example/o/dor_1") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if !IsRemoved(resp.State) {
		t.Fatal("nothing may be kept in state while the order waits for payment")
	}
}

// Applied again before paying: the order is still in flight, and the apply
// stops with the same link instead of ordering again.
func TestReapplyBeforePayingStopsAgain(t *testing.T) {
	api := newFake()
	api.domains["dom_1"] = client.Domain{ID: "dom_1", Name: "example.com", Status: "pending"}
	api.orders = []client.DomainOrder{{ID: "dor_1", DomainName: "example.com", Kind: "register", Status: client.DomainOrderAwaitingPayment, PaymentURL: ptr("https://pay.example/o/dor_1")}}
	api.registerErr = &client.APIError{Status: 409, Code: client.CodeDomainOrderInFlight}
	resp := create(t, api, planFor(testSchema(t), true, nsSet()))
	if !strings.Contains(ErrorText(resp.Diagnostics), "https://pay.example/o/dor_1") || !IsRemoved(resp.State) {
		t.Fatalf("diagnostics = %q", ErrorText(resp.Diagnostics))
	}
}

// Applied again after paying: the domain is adopted, with no second order.
func TestReapplyAfterPayingAdopts(t *testing.T) {
	api := newFake()
	api.domains["dom_1"] = client.Domain{ID: "dom_1", Name: "example.com", TLD: "com", Status: client.DomainActive, AutoRenew: true, TransferLock: true, Privacy: true, NameServerMode: client.NameServerModePantech}
	api.registerErr = &client.APIError{Status: 409, Code: client.CodeDomainAlreadyHeld}
	resp := create(t, api, planFor(testSchema(t), true, nsSet()))
	if resp.Diagnostics.HasError() || getModel(t, resp.State).ID.ValueString() != "dom_1" {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
}

func TestValidateConfigNG(t *testing.T) {
	s := testSchema(t)
	contact := func(typ, state, postal string) tftypes.Value {
		return tftypes.NewValue(contactType(s), map[string]tftypes.Value{
			"type": Str(typ), "first_name": Str("Ada"), "last_name": Str("Obi"), "organization_name": Str("Acme"), "email": Str("ada@example.com"),
			"phone": Str("+234.8012345678"), "address_line1": Str("1 Marina"), "address_line2": Str(""), "city": Str("Lagos"), "state": Str(state),
			"country_code": Str("NG"), "postal_code": Str(postal),
		})
	}
	tests := []struct {
		name, domain string
		privacy      tftypes.Value
		contact      tftypes.Value
		want         string
	}{
		{"valid .ng", "example.com.ng", tftypes.NewValue(tftypes.Bool, nil), contact("person", "Lagos", "101001"), ""},
		{"privacy off on .ng", "example.ng", boolVal(false), contact("person", "Lagos", "101001"), "Privacy is always on for .ng"},
		{"no state", "example.org.ng", boolVal(true), contact("person", "", "101001"), "Missing state"},
		{"no postal code", "example.com.ng", boolVal(true), contact("person", "Lagos", ""), "Missing postal_code"},
		{"company on name.ng", "ada.name.ng", boolVal(true), contact("company", "Lagos", "101001"), "name.ng is for people"},
		{"privacy off elsewhere is fine", "example.com", boolVal(false), contact("company", "", ""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tfsdk.Config{Schema: s, Raw: Object(s, map[string]tftypes.Value{"name": Str(tt.domain), "privacy": tt.privacy, "contact": tt.contact, "accept_terms": boolVal(true)})}
			resp := &resource.ValidateConfigResponse{}
			(&Resource{}).ValidateConfig(Ctx, resource.ValidateConfigRequest{Config: cfg}, resp)
			got := ErrorText(resp.Diagnostics)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("errors = %q, want %q", got, tt.want)
			}
		})
	}
}
