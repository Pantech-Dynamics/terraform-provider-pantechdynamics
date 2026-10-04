package instance

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the resource's attributes to Go. The tfsdk tags tie each field to
// an attribute name in the schema.
type model struct {
	ID              types.String   `tfsdk:"id"`
	Name            types.String   `tfsdk:"name"`
	PlanSlug        types.String   `tfsdk:"plan_slug"`
	ImageSlug       types.String   `tfsdk:"image_slug"`
	SSHKeyID        types.String   `tfsdk:"ssh_key_id"`
	Region          types.String   `tfsdk:"region"`
	SecurityGroupID types.String   `tfsdk:"security_group_id"`
	SubnetID        types.String   `tfsdk:"subnet_id"`
	NetworkID       types.String   `tfsdk:"network_id"`
	Tags            types.Map      `tfsdk:"tags"`
	DesiredState    types.String   `tfsdk:"desired_state"`
	ObservedState   types.String   `tfsdk:"observed_state"`
	Zone            types.String   `tfsdk:"zone"`
	PublicIPv4      types.String   `tfsdk:"public_ipv4"`
	PrivateIPv4     types.String   `tfsdk:"private_ipv4"`
	CreatedAt       types.String   `tfsdk:"created_at"`
	UpdatedAt       types.String   `tfsdk:"updated_at"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

// toCreateRequest turns the plan into an API request. Unset optional values are
// omitted so the backend applies its defaults.
func toCreateRequest(ctx context.Context, m model) (client.CreateInstanceRequest, diag.Diagnostics) {
	req := client.CreateInstanceRequest{
		Name:            m.Name.ValueString(),
		PlanSlug:        m.PlanSlug.ValueString(),
		ImageSlug:       m.ImageSlug.ValueString(),
		SSHKeyID:        knownString(m.SSHKeyID),
		Region:          knownString(m.Region),
		SecurityGroupID: knownString(m.SecurityGroupID),
		SubnetID:        knownString(m.SubnetID),
		NetworkID:       knownString(m.NetworkID),
	}

	var diags diag.Diagnostics
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		diags = m.Tags.ElementsAs(ctx, &req.Tags, false)
	}
	return req, diags
}

// knownString returns the value, or "" when it is null or unknown.
func knownString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

// fromAPIResponse builds the state from the API object. prev supplies what the
// API never returns: the timeouts block, and ssh_key_id, because an instance
// does not report which key it was created with.
func fromAPIResponse(ctx context.Context, prev model, inst *client.Instance) (model, diag.Diagnostics) {
	tags := inst.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	tagValue, diags := types.MapValueFrom(ctx, types.StringType, tags)

	sshKey := prev.SSHKeyID
	if sshKey.IsUnknown() {
		sshKey = types.StringNull()
	}

	return model{
		ID:              types.StringValue(inst.ID),
		Name:            types.StringValue(inst.Name),
		PlanSlug:        types.StringValue(inst.PlanSlug),
		ImageSlug:       types.StringValue(inst.ImageSlug),
		SSHKeyID:        sshKey,
		Region:          types.StringValue(inst.Region),
		SecurityGroupID: types.StringValue(inst.SecurityGroupID),
		SubnetID:        types.StringPointerValue(inst.SubnetID),
		NetworkID:       types.StringPointerValue(inst.NetworkID),
		Tags:            tagValue,
		DesiredState:    types.StringValue(inst.DesiredState),
		ObservedState:   types.StringValue(inst.ObservedState),
		Zone:            types.StringValue(inst.Zone),
		PublicIPv4:      types.StringPointerValue(inst.PublicIPv4),
		PrivateIPv4:     types.StringPointerValue(inst.PrivateIPv4),
		CreatedAt:       timestampValue(inst.CreatedAt),
		UpdatedAt:       timestampValue(inst.UpdatedAt),
		Timeouts:        prev.Timeouts,
	}, diags
}

// pendingModel is the state saved the moment an order is accepted, before any
// wait. It carries the id, so a timeout or a cancel cannot orphan a paid order,
// and it holds no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	tags := plan.Tags
	if tags.IsUnknown() {
		tags = types.MapNull(types.StringType)
	}
	return model{
		ID:              types.StringValue(id),
		Name:            plan.Name,
		PlanSlug:        plan.PlanSlug,
		ImageSlug:       plan.ImageSlug,
		SSHKeyID:        nullIfUnknown(plan.SSHKeyID),
		Region:          nullIfUnknown(plan.Region),
		SecurityGroupID: nullIfUnknown(plan.SecurityGroupID),
		SubnetID:        nullIfUnknown(plan.SubnetID),
		NetworkID:       nullIfUnknown(plan.NetworkID),
		Tags:            tags,
		DesiredState:    nullIfUnknown(plan.DesiredState),
		ObservedState:   types.StringNull(),
		Zone:            types.StringNull(),
		PublicIPv4:      types.StringNull(),
		PrivateIPv4:     types.StringNull(),
		CreatedAt:       types.StringNull(),
		UpdatedAt:       types.StringNull(),
		Timeouts:        plan.Timeouts,
	}
}

func nullIfUnknown(v types.String) types.String {
	if v.IsUnknown() {
		return types.StringNull()
	}
	return v
}

// timestampValue formats a time to whole seconds. Whole seconds keep state
// stable if the backend varies the fractional digits, as it does elsewhere.
func timestampValue(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}
