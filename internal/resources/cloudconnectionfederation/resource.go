// Package cloudconnectionfederation implements the
// pantechdynamics_cloud_connection_federation resource: what a federated
// connection's setup printed (an AWS role ARN, or the Google Cloud, Azure or
// Huawei Cloud ids), sent so Pantech can verify its read-only access. It is
// separate from pantechdynamics_cloud_connection so one configuration can
// create the connection, run its setup with the other cloud's provider, and
// feed the result back without a cycle.
package cloudconnectionfederation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	kind           = "cloud connection"
	defaultTimeout = 10 * time.Minute
)

// federationAPI is the part of the API client this resource needs.
type federationAPI interface {
	GetCloudConnection(ctx context.Context, id string) (*client.CloudConnection, error)
	UpdateCloudConnection(ctx context.Context, id string, req client.UpdateCloudConnectionRequest) (*client.CloudConnection, error)
	WaitForCloudConnectionCheck(ctx context.Context, id string) (*client.CloudConnection, error)
}

var (
	_ resource.Resource                   = &Resource{}
	_ resource.ResourceWithConfigure      = &Resource{}
	_ resource.ResourceWithImportState    = &Resource{}
	_ resource.ResourceWithValidateConfig = &Resource{}
)

// Resource sends a connection's federation and waits for the check.
type Resource struct {
	api federationAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

type gcpModel struct {
	ProjectNumber       types.String   `tfsdk:"project_number"`
	PoolID              types.String   `tfsdk:"pool_id"`
	ProviderID          types.String   `tfsdk:"provider_id"`
	ServiceAccountEmail types.String   `tfsdk:"service_account_email"`
	ProjectIDs          []types.String `tfsdk:"project_ids"`
}

type azureModel struct {
	TenantID        types.String   `tfsdk:"tenant_id"`
	ClientID        types.String   `tfsdk:"client_id"`
	SubscriptionIDs []types.String `tfsdk:"subscription_ids"`
}

type huaweiProjectModel struct {
	Region types.String `tfsdk:"region"`
	ID     types.String `tfsdk:"id"`
}

type huaweiModel struct {
	IdentityProviderID types.String         `tfsdk:"identity_provider_id"`
	Projects           []huaweiProjectModel `tfsdk:"projects"`
}

type model struct {
	ID                types.String   `tfsdk:"id"`
	ConnectionID      types.String   `tfsdk:"connection_id"`
	RoleARN           types.String   `tfsdk:"role_arn"`
	GCP               *gcpModel      `tfsdk:"gcp"`
	Azure             *azureModel    `tfsdk:"azure"`
	Huawei            *huaweiModel   `tfsdk:"huawei"`
	Status            types.String   `tfsdk:"status"`
	ExternalAccountID types.String   `tfsdk:"external_account_id"`
	VerifiedAt        types.String   `tfsdk:"verified_at"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the type name: pantechdynamics_cloud_connection_federation.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_connection_federation"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Description: desc, Required: true}
	}
	list := func(desc string) schema.ListAttribute {
		return schema.ListAttribute{Description: desc, Required: true, ElementType: types.StringType}
	}
	resp.Schema = schema.Schema{
		Description: "Sends what a federated pantechdynamics_cloud_connection's setup printed, exactly one of role_arn (AWS), gcp, azure or huawei, and waits until Pantech has verified its read-only access. " +
			"These are ids, never access keys. Every argument changes in place, and each change is verified again. A check that fails stops the apply with what to do, and the next apply sends it again. " +
			"Destroying it only removes it from state: Pantech keeps the access until the connection is destroyed, or until it is removed in the other cloud.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Description: "The connection's id.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"connection_id": schema.StringAttribute{
				Description:   "The pantechdynamics_cloud_connection's id (ccn_…). Changing it replaces this resource.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{resourcekit.IDPrefix(kind, "ccn_")},
			},
			"role_arn": schema.StringAttribute{Description: "AWS: the stack's RoleArn output, like arn:aws:iam::123456789012:role/name.", Optional: true},
			"gcp": schema.SingleNestedAttribute{
				Description: "Google Cloud: what the connection's gcloud script printed.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"project_number":        str("The workload identity pool's project number."),
					"pool_id":               str("The workload identity pool's id."),
					"provider_id":           str("The pool provider's id."),
					"service_account_email": str("The service account's email."),
					"project_ids":           list("The projects to scan, 1 to 20."),
				},
			},
			"azure": schema.SingleNestedAttribute{
				Description: "Azure: what the connection's Azure CLI script printed.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"tenant_id":        str("The tenant id."),
					"client_id":        str("The app registration's application (client) id."),
					"subscription_ids": list("The subscriptions to scan, 1 to 20."),
				},
			},
			"huawei": schema.SingleNestedAttribute{
				Description: "Huawei Cloud: the identity provider made from the connection's steps, and the region projects to scan.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"identity_provider_id": str("The identity provider's id."),
					"projects": schema.ListNestedAttribute{
						Description: "The region projects to scan, 1 to 20.",
						Required:    true,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"region": str("The region, such as \"af-south-1\"."),
							"id":     str("The region project's id."),
						}},
					},
				},
			},
			"status":              schema.StringAttribute{Description: "The connection's status after the check: \"connected\", or \"failed\".", Computed: true},
			"external_account_id": schema.StringAttribute{Description: "The source account the check reached.", Computed: true},
			"verified_at":         schema.StringAttribute{Description: "When the connection was verified, in RFC 3339 UTC, to the second.", Computed: true},
			"timeouts":            timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

// ValidateConfig requires exactly one of role_arn, gcp, azure and huawei.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var roleARN types.String
	var gcp, azure, huawei types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("role_arn"), &roleARN)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("gcp"), &gcp)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("azure"), &azure)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("huawei"), &huawei)...)
	if resp.Diagnostics.HasError() {
		return
	}
	set, unknown := 0, false
	for _, a := range []interface {
		IsNull() bool
		IsUnknown() bool
	}{roleARN, gcp, azure, huawei} {
		if a.IsUnknown() {
			unknown = true
		} else if !a.IsNull() {
			set++
		}
	}
	if unknown {
		return
	}
	if set != 1 {
		resp.Diagnostics.AddError("Exactly one federation", "Set exactly one of role_arn (AWS), gcp, azure or huawei: what the connection's setup printed.")
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(federationAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create sends the federation and waits for the check.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.send(ctx, plan, plan.Timeouts.Create, &resp.State, &resp.Diagnostics)
}

// Update sends the changed federation and waits for the check again.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.send(ctx, plan, plan.Timeouts.Update, &resp.State, &resp.Diagnostics)
}

type stateSetter interface {
	Set(ctx context.Context, val any) diag.Diagnostics
}

// send PATCHes the connection, saves state at once, and waits for the check.
// A failed check is an error after state is saved, so Terraform marks the
// resource tainted and the next apply sends it again.
func (r *Resource) send(ctx context.Context, plan model, get func(context.Context, time.Duration) (time.Duration, diag.Diagnostics), st stateSetter, diags *diag.Diagnostics) {
	d, td := get(ctx, defaultTimeout)
	diags.Append(td...)
	if diags.HasError() {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	id := plan.ConnectionID.ValueString()
	conn, err := r.api.UpdateCloudConnection(waitCtx, id, toUpdateRequest(plan))
	if err != nil {
		resourcekit.AddHintedAPIError(diags, "Error sending the cloud connection's federation", err, attributeFor, map[string]string{
			client.CodeMigrateUnavailable: "Pantech Migrate is not available on this platform yet.",
		})
		return
	}
	diags.Append(st.Set(ctx, withStatus(plan, conn))...)
	if conn.Status == client.CloudConnectionVerifying {
		if conn, err = r.api.WaitForCloudConnectionCheck(waitCtx, id); err != nil {
			resourcekit.AddWaitError(diags, "Error waiting for the cloud connection's check", kind, id, err)
			return
		}
		diags.Append(st.Set(ctx, withStatus(plan, conn))...)
	}
	if conn.Status == client.CloudConnectionFailed {
		msg := fmt.Sprintf("Pantech could not verify cloud connection %s.", id)
		if f := conn.Failure; f != nil {
			msg += " " + f.Message + " (" + f.Code + ")"
		}
		diags.AddError("Cloud connection check failed", msg+"\n\nFix it in the other cloud, then apply again.")
	}
}

// Read refreshes the status, and the federation the connection holds.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	conn, err := r.api.GetCloudConnection(ctx, state.ConnectionID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading cloud connection", err.Error())
		return
	}
	m := withStatus(state, conn)
	fromFederation(&m, conn)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// Delete only removes the resource from state: the API cannot take a
// verified federation back.
func (r *Resource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Cloud connection federation removed from state only",
		"Pantech keeps this connection's access. Destroy the pantechdynamics_cloud_connection, or remove the trust in the other cloud, to end it.")
}

// ImportState adopts a connection's federation by the connection's id.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, kind, "ccn_", req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("connection_id"), req.ID)...)
}

func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "role_arn", "gcp", "azure", "huawei":
		return path.Root(field), true
	}
	return path.Empty(), false
}
