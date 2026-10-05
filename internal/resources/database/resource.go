// Package database implements the pantechdynamics_database resource.
package database

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	// idPrefix is the prefix every database id carries.
	idPrefix = "db_"

	// A database is paid for first and then provisioned on its own VM, which
	// takes as long as an instance or longer.
	defaultCreateTimeout = 45 * time.Minute
	defaultUpdateTimeout = 20 * time.Minute
	defaultDeleteTimeout = 30 * time.Minute
)

// databaseAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type databaseAPI interface {
	CreateDatabase(ctx context.Context, req client.CreateDatabaseRequest) (*client.DatabaseOrderReference, error)
	GetDatabase(ctx context.Context, id string) (*client.Database, error)
	ListDatabases(ctx context.Context) ([]client.Database, error)
	DeleteDatabase(ctx context.Context, id string) (*client.OperationReference, error)
	StartDatabase(ctx context.Context, id string) (*client.OperationReference, error)
	StopDatabase(ctx context.Context, id string) (*client.OperationReference, error)
	ReplaceDatabaseAccessRules(ctx context.Context, id string, cidrs []string) (*client.OperationReference, error)
	SetDatabaseSecurityGroups(ctx context.Context, id string, groupIDs []string) (*client.OperationReference, error)
	ChangeDatabasePassword(ctx context.Context, id, password string) (*client.OperationReference, error)
	ResizeDatabaseStorage(ctx context.Context, id string, storageGB int64) (*client.OperationReference, error)
	WaitForDatabaseOrder(ctx context.Context, id string) (*client.DatabaseOrder, error)
	WaitForOperation(ctx context.Context, id string, done client.DoneCheck) error
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
	_ resource.ResourceWithModifyPlan  = &Resource{}
)

// Resource manages one managed database.
type Resource struct {
	api databaseAPI
}

// New is the factory the provider registers.
func New() resource.Resource {
	return &Resource{}
}

// Metadata sets the resource type name: pantechdynamics_database.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A managed PostgreSQL, MySQL or MariaDB database on its own VM, reachable only from private networks and the CIDRs in access_rules. Creating one places an order that is paid first, from account credit or the default card, like an instance of the same plan, so it costs money. " +
			"The admin password is the write-only argument password_wo, so it is never stored in plan or state; this needs Terraform 1.11 or later. Access rules, security groups, the storage size (grow only), the password (through password_wo_version) and the power state can be changed in place. Every other argument replaces the database, which destroys its data.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the database, starting with db_.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"name": schema.StringAttribute{
				Description:   "Name of the database, 1 to 255 characters, unique in the account. Changing it replaces the database.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.NameLength("database", 255)},
			},
			"engine": schema.StringAttribute{
				Description:   "Database engine: \"postgresql\", \"mysql\" or \"mariadb\". See the pantechdynamics_database_engines data source. Changing it replaces the database.",
				Required:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.OneOf(client.EnginePostgreSQL, client.EngineMySQL, client.EngineMariaDB)},
			},
			"version": schema.StringAttribute{
				Description:   "Engine version line, for example \"18\", from the pantechdynamics_database_engines data source. Changing it replaces the database.",
				Required:      true,
				PlanModifiers: replace,
			},
			"plan_slug": schema.StringAttribute{
				Description: "Plan that sizes the database (vCPU, memory, and the plan's disk_gb as the data volume), from the pantechdynamics_plans data source. The database is billed as an instance of this plan with no public address. The API reports the plan only by id, so a change made outside Terraform is not detected, and after an import this holds no value and is not compared. Changing it replaces the database.",
				Required:    true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(
					replaceUnlessImported,
					"Replaces the database when the plan changes, but not when the state holds none (after an import).",
					"Replaces the database when the plan changes, but not when the state holds none (after an import).",
				)},
			},
			"zone_id": schema.StringAttribute{
				Description:   "Zone to create the database in, for example \"af-abj-2\" (a VPC zone, which needs subnet_id) or \"af-abj-1\" (a standard zone, which takes no subnet_id). See the pantechdynamics_regions data source. Changing it replaces the database.",
				Required:      true,
				PlanModifiers: replace,
			},
			"subnet_id": schema.StringAttribute{
				Description:   "Id of the VPC subnet to place the database in, from pantechdynamics_subnet. Required in a VPC zone, and must be omitted in a standard zone. Changing it replaces the database.",
				Optional:      true,
				PlanModifiers: replace,
				Validators:    []validator.String{resourcekit.IDPrefix("subnet", "snet_")},
			},
			"admin_username": schema.StringAttribute{
				Description:   "Admin login to connect with: 3 to 32 lowercase letters, digits or underscores, starting with a letter. Reserved names (root, postgres, public, and names starting with pg_, mysql, mariadb or pantech) are refused. Defaults to \"dbadmin\". It is not a superuser: on PostgreSQL and MySQL it can create further users and databases; on MariaDB it is the only login. Changing it replaces the database.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(client.DefaultDatabaseAdminUsername),
				PlanModifiers: replace,
				Validators:    []validator.String{adminUsernameValidator{}},
			},
			"access_rules": schema.SetAttribute{
				Description: "IPv4 CIDRs allowed to connect to the engine's port, at most 50, each /8 or longer (0.0.0.0/0 is refused). This is the database's own allow-list (security_group_ids can add more ranges; effective_access_rules shows the result): changing it replaces every rule in place, and removing a CIDR also ends connections already open from it. Omit it to keep the zone's default (the subnet's VPC CIDR in a VPC zone, none in a standard zone) and whatever the platform reports; set [] for no access.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.Set{accessRulesValidator{}},
			},
			"security_group_ids": schema.SetAttribute{
				Description: "Ids of up to 5 of your security groups, from pantechdynamics_security_group, whose rules add allowed ranges alongside access_rules. Only ingress rules for tcp or all protocols whose ports include the engine's port and whose CIDR is IPv4 /8 or longer count; every other rule is listed in ignored_security_group_rules. The groups are never attached to the database server, and when a group's rules change the database follows automatically. Changing this list replaces the whole set in place; [] detaches every group. A group cannot be deleted while a database uses it.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.Set{securityGroupIDsValidator{}},
			},
			"effective_access_rules": schema.ListNestedAttribute{
				Description: "The allow-list as the database applies it: its own access_rules plus the ranges its security groups add, each range once under its first source.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"cidr":     schema.StringAttribute{Computed: true, Description: "IPv4 range allowed."},
					"protocol": schema.StringAttribute{Computed: true, Description: "Always \"tcp\"."},
					"port":     schema.Int64Attribute{Computed: true, Description: "The engine's port."},
					"source":   schema.StringAttribute{Computed: true, Description: "\"access_rule\", or \"security_group:\" followed by the group's id."},
				}},
			},
			"ignored_security_group_rules": schema.ListNestedAttribute{
				Description: "Rules of the attached security groups that do not apply to the database, and why.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"security_group_id": schema.StringAttribute{Computed: true, Description: "The group the rule belongs to."},
					"direction":         schema.StringAttribute{Computed: true, Description: "\"ingress\" or \"egress\"."},
					"protocol":          schema.StringAttribute{Computed: true, Description: "\"tcp\", \"udp\", \"icmp\" or \"all\"."},
					"port_range":        schema.StringAttribute{Computed: true, Description: "A port or range, or null for all ports."},
					"cidr":              schema.StringAttribute{Computed: true, Description: "The rule's range."},
					"reason":            schema.StringAttribute{Computed: true, Description: "egress_rule, protocol_not_tcp, port_not_covered, cidr_too_wide or cidr_not_ipv4."},
				}},
			},
			"desired_state": schema.StringAttribute{
				Description: "Whether the database should be \"running\" or \"stopped\". Defaults to \"running\". Changing it starts or stops the database in place. A password change needs it running, so it is started for the change and stopped again if it should be stopped.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(client.DatabaseRunning),
				Validators:  []validator.String{resourcekit.OneOf(client.DatabaseRunning, client.DatabaseStopped)},
			},
			"password_wo": schema.StringAttribute{
				Description: "Admin password, 16 to 128 printable ASCII characters with no spaces, quotes or backslashes. Write-only: Terraform sends it to the API and never stores it in plan or state, and the API never returns it. Requires Terraform 1.11 or later. It is required because a password the API generates is returned only once and could not be kept out of state. Terraform cannot see a change to a write-only value: to change the password, change password_wo and increase password_wo_version together. Read it from a secret store or an ephemeral resource, not a literal.",
				Required:    true,
				Sensitive:   true,
				WriteOnly:   true,
				Validators:  []validator.String{passwordValidator{}},
			},
			"password_wo_version": schema.Int64Attribute{
				Description: "Any number that you change whenever password_wo changes. A change sets the database's admin password to password_wo in place (the database must be running, and is started for it if needed). After an import it holds no value, so setting it sets the password.",
				Optional:    true,
			},
			"plan_id": schema.StringAttribute{
				Description:   "Identifier of the plan the database runs on.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"port": schema.Int64Attribute{
				Description:   "TCP port the engine listens on, for example 5432 for PostgreSQL.",
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"hostname": schema.StringAttribute{
				Description:   "Hostname to connect to, for example db-7k2q9x4m1a.af-abj-2.db.pantechdynamics.com. It resolves to a private address only.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"private_ip": schema.StringAttribute{
				Description:   "Private IPv4 address of the database.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"storage_gb": schema.Int64Attribute{
				Description: "Size of the data disk in GB. Omit it for the plan's disk_gb. At least the plan's disk_gb and at most the zone's maximum (2000 GB); above the plan's size it must be a multiple of the zone's step (10 GB), which the API checks. The upfront payment and the hourly storage price use this size. " +
					"Increasing it grows the disk in place, online and without a restart (the database must be running, so a stopped one is started for it and stopped again); storage is billed at the new size once the resize completes. It can never shrink: a smaller value is an error at plan time, and the database is never replaced for it. A resize made outside Terraform shows here after a refresh.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.Int64{resourcekit.IntBetween(1, maxStorageGB)},
			},
			"data_volume_size_gb": schema.Int64Attribute{
				Description: "Size of the data disk in GB as in place and billed. It changes once a storage resize completes.",
				Computed:    true,
			},
			"observed_state": schema.StringAttribute{
				Description: "State of the database as the platform sees it, for example \"running\".",
				Computed:    true,
			},
			"failure_code": schema.StringAttribute{
				Description: "Why the database failed, when it has.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the database was created, in RFC 3339 UTC, to the second.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"updated_at": schema.StringAttribute{
				Description: "When the database was last changed, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// replaceUnlessImported replaces the database when the plan slug differs from
// the state, but not when the state holds none. The API reports plan_id, not
// the slug, so an imported database has none in state, and replacing it for
// that would destroy its data to fix a gap in our own state.
func replaceUnlessImported(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull() && !req.ConfigValue.Equal(req.StateValue)
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(databaseAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create places the order, saves the database id straight away, then waits for
// the order to be paid and provisioned and for the database to run. Saving
// first means a timeout cannot orphan an order that has been paid for.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	password, ok := configPassword(ctx, req.Config, &resp.Diagnostics)
	if !ok {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Create, defaultCreateTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	createReq, diags := toCreateRequest(ctx, plan, password)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	placed, err := placeOrder(ctx, r.api, createReq)
	if err != nil {
		addCreateError(&resp.Diagnostics, err)
		return
	}
	id := placed.databaseID()
	resp.Diagnostics.Append(resp.State.Set(ctx, pendingModel(plan, id, placed.adminUsername()))...)

	opID := ""
	if placed.Order != nil {
		order, err := r.api.WaitForDatabaseOrder(ctx, placed.Order.OrderID)
		if err != nil {
			addWaitError(&resp.Diagnostics, "Error creating database", id, err)
			return
		}
		if order.OperationID != nil {
			opID = *order.OperationID
		}
	}
	if err := r.waitFor(ctx, opID, "database "+id+" to run", r.hasObservedState(id, client.DatabaseRunning)); err != nil {
		addWaitError(&resp.Diagnostics, "Error creating database", id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The create request takes no security groups, so they are attached after.
	if groups := knownSet(plan.SecurityGroupIDs); len(groups) > 0 {
		if err := r.setSecurityGroups(ctx, id, groups); err != nil {
			addUpdateError(&resp.Diagnostics, "Error attaching security groups to the new database", id, err)
			return
		}
		r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if plan.DesiredState.ValueString() != client.DatabaseStopped {
		return
	}

	// A database is always created running, so a requested stop comes after.
	if err := r.applyPower(ctx, id, client.DatabaseStopped); err != nil {
		addWaitError(&resp.Diagnostics, "Error stopping the new database", id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// Read refreshes state. A database that is gone or reported as deleted is
// removed from state, so the next plan recreates it.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	db, err := r.api.GetDatabase(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && db.ObservedState == client.DatabaseDeleted) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error reading database", err, nil)
		return
	}
	next, diags := fromAPIResponse(state, db)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

// Update applies the in-place changes: access rules, security groups, storage,
// password and power state. A storage resize or a password change needs the
// database running, so it is started first when
// needed, and the last step brings it to the state the configuration asks for.
// State is refreshed after each step, so a step that succeeded is not lost if a
// later one fails.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, plan.Timeouts.Update, defaultUpdateTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	rulesChanged := !plan.AccessRules.IsUnknown() && !plan.AccessRules.IsNull() && !plan.AccessRules.Equal(state.AccessRules)
	groupsChanged := !plan.SecurityGroupIDs.IsUnknown() && !plan.SecurityGroupIDs.IsNull() && !plan.SecurityGroupIDs.Equal(state.SecurityGroupIDs)
	passwordChanged := !plan.PasswordWOVersion.IsNull() && !plan.PasswordWOVersion.Equal(state.PasswordWOVersion)
	storageGrows := !plan.StorageGB.IsNull() && !plan.StorageGB.IsUnknown() &&
		(state.StorageGB.IsNull() || plan.StorageGB.ValueInt64() > state.StorageGB.ValueInt64())

	var password string
	if passwordChanged {
		if password, ok = configPassword(ctx, req.Config, &resp.Diagnostics); !ok {
			return
		}
	}

	if rulesChanged {
		cidrs, diags := cidrsOf(ctx, plan.AccessRules)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		if err := r.replaceAccessRules(ctx, id, cidrs); err != nil {
			addUpdateError(&resp.Diagnostics, "Error replacing the access rules", id, err)
			return
		}
	}
	if groupsChanged {
		if err := r.setSecurityGroups(ctx, id, knownSet(plan.SecurityGroupIDs)); err != nil {
			addUpdateError(&resp.Diagnostics, "Error setting the security groups", id, err)
			return
		}
	}
	if storageGrows {
		if err := r.resizeStorage(ctx, id, plan.StorageGB.ValueInt64()); err != nil {
			addUpdateError(&resp.Diagnostics, "Error growing the database storage", id, err)
			r.refresh(ctx, state, id, &resp.State, &resp.Diagnostics)
			return
		}
	}
	if passwordChanged {
		if err := r.applyPower(ctx, id, client.DatabaseRunning); err != nil {
			addUpdateError(&resp.Diagnostics, "Error starting the database to change its password", id, err)
			return
		}
		if err := r.changePassword(ctx, id, password); err != nil {
			addUpdateError(&resp.Diagnostics, "Error changing the database password", id, err)
			r.refresh(ctx, state, id, &resp.State, &resp.Diagnostics)
			return
		}
	}
	if err := r.applyPower(ctx, id, plan.DesiredState.ValueString()); err != nil {
		addUpdateError(&resp.Diagnostics, "Error changing the database power state", id, err)
		return
	}
	r.refresh(ctx, plan, id, &resp.State, &resp.Diagnostics)
}

// Delete removes the database and waits until it is gone. A database that is
// already gone counts as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel, ok := withTimeout(ctx, state.Timeouts.Delete, defaultDeleteTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	id := state.ID.ValueString()
	ref, err := r.api.DeleteDatabase(ctx, id)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddAPIError(&resp.Diagnostics, "Error deleting database", err, nil)
		return
	}
	if err := r.api.WaitForOperation(ctx, ref.OperationID, r.isGone(id)); err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error deleting database", "database", id, err)
	}
}

// ImportState adopts an existing database by id. Its password is not imported,
// because the API never returns it.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, "database", idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// refresh reads the database and stores it, keeping what only Terraform knows
// from prev.
func (r *Resource) refresh(ctx context.Context, prev model, id string, st *tfsdk.State, diags *diag.Diagnostics) {
	db, err := r.api.GetDatabase(ctx, id)
	if err != nil {
		resourcekit.AddAPIError(diags, "Error reading database after the change", err, nil)
		return
	}
	next, d := fromAPIResponse(prev, db)
	diags.Append(d...)
	diags.Append(st.Set(ctx, next)...)
}

// configPassword reads the write-only password from the configuration, the only
// place it exists. It is never logged.
func configPassword(ctx context.Context, cfg tfsdk.Config, diags *diag.Diagnostics) (string, bool) {
	var pw types.String
	diags.Append(cfg.GetAttribute(ctx, pathPassword, &pw)...)
	if diags.HasError() {
		return "", false
	}
	if pw.IsNull() || pw.IsUnknown() {
		diags.AddAttributeError(pathPassword, "Missing database password",
			"password_wo must be set to a known value when the database is created or password_wo_version changes. Write-only arguments need Terraform 1.11 or later.")
		return "", false
	}
	return pw.ValueString(), true
}

// withTimeout applies the configured timeout, or fallback, to ctx.
func withTimeout(ctx context.Context, get resourcekit.Timeout, fallback time.Duration, diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, fallback)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}
