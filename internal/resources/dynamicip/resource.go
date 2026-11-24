// Package dynamicip implements the pantechdynamics_dynamic_ip resource.
package dynamicip

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const (
	kind = "Dynamic IP"

	defaultCreateTimeout = 15 * time.Minute
	defaultDeleteTimeout = 5 * time.Minute
)

// dynamicIPAPI is the part of the API client this resource needs. It is defined
// here, by the consumer, so tests can fake it.
type dynamicIPAPI interface {
	GetDynamicIP(ctx context.Context, instanceID string) (*client.DynamicIP, error)
	PutDynamicIP(ctx context.Context, instanceID string, req client.PutDynamicIPRequest) (*client.DynamicIP, error)
	DeleteDynamicIP(ctx context.Context, instanceID string) (*client.DynamicIP, error)
	WaitUntil(ctx context.Context, what string, done client.DoneCheck) error
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

// Resource manages Dynamic IP on one instance.
type Resource struct {
	api dynamicIPAPI
}

// New is the factory the provider registers.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource type name: pantechdynamics_dynamic_ip.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dynamic_ip"
}

// Schema describes the attributes.
func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Dynamic IP for a running standard instance: outbound traffic leaves from a public IPv4 address that rotates on the chosen schedule, while inbound traffic continues to reach the instance's static address. " +
			"Requires a standard instance (not in a VPC subnet) running a supported image with the platform agent and a security group whose inbound rules exclude the zone's private network. " +
			"While enabled, a schedule surcharge is added to the instance's hourly price: every_12_hours (+60%), daily (+50%), weekly (+40%), or monthly (+30%). " +
			"Changes to schedule, connections, and on_demand apply in place without replacing the instance. Do not manage Dynamic IP for the same instance with both this resource and pantechdynamics_instance.dynamic_ip.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Identifier of the Dynamic IP configuration, equal to instance_id.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"instance_id": schema.StringAttribute{
				Description:   "Id of the instance to enable Dynamic IP on, starting with vm_. Changing it replaces this resource.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{instanceIDValidator()},
			},
			"schedule": schema.StringAttribute{
				Description: "How often the outbound public address rotates: \"every_12_hours\", \"daily\", \"weekly\", or \"monthly\". Changes in place.",
				Required:    true,
				Validators:  []validator.String{scheduleValidator()},
			},
			"connections": schema.StringAttribute{
				Description: "How existing outbound connections behave on rotation: \"keep\" (default) lets open connections finish on the previous address; \"move\" switches them to the new address immediately. Changes in place.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(client.DynamicIPConnectionsKeep),
				Validators:  []validator.String{connectionsValidator()},
			},
			"on_demand": schema.BoolAttribute{
				Description: "Whether on-demand rotation is allowed for this instance. Requires on-demand rotation to be enabled for your organization. Defaults to false. Changes in place.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"state": schema.StringAttribute{
				Description: "Current lifecycle state of Dynamic IP: \"enabling\", \"active\", \"degraded\", \"disabling\", \"off\", or \"failed\".",
				Computed:    true,
			},
			"current_address": schema.StringAttribute{
				Description: "The rotating public IPv4 address the instance currently uses for outbound traffic, or null before the first address is ready or while falling back to the instance's own address.",
				Computed:    true,
			},
			"next_rotation_at": schema.StringAttribute{
				Description: "When the next scheduled rotation is due, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"last_rotated_at": schema.StringAttribute{
				Description: "When the outbound address last rotated, in RFC 3339 UTC, to the second.",
				Computed:    true,
			},
			"surcharge_percent": schema.Float64Attribute{
				Description: "Percentage added to the instance's hourly price while Dynamic IP is enabled.",
				Computed:    true,
			},
			"on_demand_used": schema.Int64Attribute{
				Description: "Number of on-demand rotations performed in the last 24 hours.",
				Computed:    true,
			},
			"on_demand_limit": schema.Int64Attribute{
				Description: "Maximum number of on-demand rotations allowed per 24 hours.",
				Computed:    true,
			},
			"failure_reason": schema.StringAttribute{
				Description: "Explanation when state is \"failed\" or \"degraded\", or null otherwise.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

// Configure receives the API client built by the provider.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(dynamicIPAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	r.api = api
}

// Create enables Dynamic IP on the instance, saves pending state immediately so
// a timeout cannot orphan the change, and polls until it reaches active or
// degraded.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	waitCtx, cancel, ok := withTimeout(ctx, plan.Timeouts.Create, defaultCreateTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	instanceID := plan.InstanceID.ValueString()
	initial, err := r.api.PutDynamicIP(waitCtx, instanceID, toPutRequest(plan))
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error enabling Dynamic IP", err, attributeFor, writeHints)
		return
	}

	// Save pending state immediately before polling so a timeout or cancel never
	// orphans an accepted Dynamic IP enable request.
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, initial))...)
	if resp.Diagnostics.HasError() {
		return
	}

	if initial.State == client.DynamicIPStateActive || initial.State == client.DynamicIPStateDegraded {
		return
	}
	if initial.State == client.DynamicIPStateFailed {
		resp.Diagnostics.AddError("Error enabling Dynamic IP", failedError(instanceID, initial.FailureReason).Error())
		return
	}

	latest := initial
	err = r.api.WaitUntil(waitCtx, kind+" on "+instanceID+" to become active", func(ctx context.Context) (bool, error) {
		got, getErr := r.api.GetDynamicIP(ctx, instanceID)
		if errors.Is(getErr, client.ErrNotFound) {
			return false, nil
		}
		if getErr != nil {
			return false, getErr
		}
		latest = got
		switch got.State {
		case client.DynamicIPStateActive, client.DynamicIPStateDegraded:
			return true, nil
		case client.DynamicIPStateFailed:
			return false, failedError(instanceID, got.FailureReason)
		case client.DynamicIPStateOff:
			return false, failedError(instanceID, got.FailureReason)
		}
		return false, nil
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, latest))...)
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error enabling Dynamic IP", kind, instanceID, err)
	}
}

// Read refreshes state from the API. If Dynamic IP is not found or off, the
// resource is removed from state so the next plan recreates it.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := state.InstanceID.ValueString()
	if instanceID == "" {
		instanceID = state.ID.ValueString()
	}

	got, err := r.api.GetDynamicIP(ctx, instanceID)
	if errors.Is(err, client.ErrNotFound) || (err == nil && got.State == client.DynamicIPStateOff) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading Dynamic IP", err, readHints)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(state, got))...)
}

// Update changes schedule, connections, or on_demand in place and refreshes
// state from the response.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := state.InstanceID.ValueString()
	updated, err := r.api.PutDynamicIP(ctx, instanceID, toPutRequest(plan))
	if err != nil {
		resourcekit.AddHintedAPIError(&resp.Diagnostics, "Error updating Dynamic IP", err, attributeFor, writeHints)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(plan, updated))...)
}

// Delete disables Dynamic IP and polls until the API reports ErrNotFound or
// state "off".
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	waitCtx, cancel, ok := withTimeout(ctx, state.Timeouts.Delete, defaultDeleteTimeout, &resp.Diagnostics)
	if !ok {
		return
	}
	defer cancel()

	instanceID := state.InstanceID.ValueString()
	if instanceID == "" {
		instanceID = state.ID.ValueString()
	}

	res, err := r.api.DeleteDynamicIP(waitCtx, instanceID)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error disabling Dynamic IP", err, deleteHints)
		return
	}
	if res != nil && res.State == client.DynamicIPStateOff {
		return
	}

	err = r.api.WaitUntil(waitCtx, kind+" on "+instanceID+" to turn off", func(ctx context.Context) (bool, error) {
		got, getErr := r.api.GetDynamicIP(ctx, instanceID)
		if errors.Is(getErr, client.ErrNotFound) {
			return true, nil
		}
		if getErr != nil {
			return false, getErr
		}
		if got.State == client.DynamicIPStateOff {
			return true, nil
		}
		if got.State == client.DynamicIPStateFailed {
			return false, failedError(instanceID, got.FailureReason)
		}
		return false, nil
	})
	if err != nil {
		resourcekit.AddWaitError(&resp.Diagnostics, "Error disabling Dynamic IP", kind, instanceID, err)
	}
}

// ImportState adopts an existing Dynamic IP configuration by its instance id
// (vm_...).
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !resourcekit.CheckID(&resp.Diagnostics, "instance", idPrefix, req.ID) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), req.ID)...)
}

func withTimeout(ctx context.Context, get resourcekit.Timeout, fallback time.Duration, diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, fallback)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}

func failedError(instanceID string, reason *string) error {
	if reason != nil && *reason != "" {
		return fmt.Errorf("dynamic IP on instance %s failed: %s", instanceID, *reason)
	}
	return fmt.Errorf("dynamic IP on instance %s failed", instanceID)
}

func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "schedule", "connections", "on_demand":
		return path.Root(field), true
	default:
		return path.Path{}, false
	}
}

var (
	writeHints = map[string]string{
		client.CodeAddressRotationNotEnabled:          "Dynamic IP is not enabled for your organization yet. Contact Pantech Dynamics support to request access.",
		client.CodeAddressRotationOnDemandOff:         "On-demand rotation (on_demand = true) is not enabled for your organization. Set on_demand = false or contact Pantech Dynamics support.",
		client.CodeAddressRotationNeedsPrivateNetwork: "Dynamic IP requires a standard instance (not in a VPC subnet) in a zone that supports private networking.",
		client.CodeAddressRotationNeedsAgent:          "Dynamic IP requires the platform agent on the instance. Create the instance from a supported image that includes the agent.",
		client.CodeAddressRotationNeedsSecurityGroup:  "Dynamic IP requires a security group whose inbound rules allow nothing from the zone's private network range. Narrow the ingress rules in the instance's pantechdynamics_security_group and apply again.",
		client.CodeAddressRotationNoAddress:           "No rotating public IP addresses are currently available in this zone. Try again later or contact Pantech Dynamics support.",
		client.CodeAddressRotationUnavailable:         "Dynamic IP is not available in this instance's zone.",
		client.CodeAddressRotationBusy:                "A Dynamic IP change or rotation is already in progress on this instance. Wait for it to finish, then apply again.",
		client.CodeAddressRotationNotFound:            "The instance does not exist, or Dynamic IP is not enabled on it.",
		client.CodeAddressRotationLimitReached:        "The 24-hour limit for on-demand rotations has been reached. Wait before rotating again.",
		"INSTANCE_NOT_RUNNING":                        "The instance must be running to enable or change Dynamic IP. Start the instance and apply again.",
		"FORBIDDEN":                                   "Your role is not permitted to manage Dynamic IP. Use an API key created by an owner, admin, or developer.",
		"INSUFFICIENT_SCOPE":                          "Managing Dynamic IP needs an API key with the write scope.",
		"ACCESS_DENIED":                               "An access policy refuses this action. Ask an owner to grant permission for instance Dynamic IP.",
	}
	readHints = map[string]string{
		client.CodeAddressRotationNotFound: "Dynamic IP is not enabled on this instance, or the instance does not exist.",
		"FORBIDDEN":                        "Your role is not permitted to read Dynamic IP.",
	}
	deleteHints = map[string]string{
		client.CodeAddressRotationBusy: "A Dynamic IP change or rotation is already in progress on this instance. Wait for it to finish, then try again.",
		"FORBIDDEN":                    "Your role is not permitted to disable Dynamic IP. Use an API key created by an owner, admin, or developer.",
		"INSUFFICIENT_SCOPE":           "Disabling Dynamic IP needs an API key with the write or delete scope.",
		"ACCESS_DENIED":                "An access policy refuses disabling Dynamic IP. Ask an owner for permission.",
	}
)
