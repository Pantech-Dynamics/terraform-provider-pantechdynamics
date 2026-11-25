package cloudconnection

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go.
type model struct {
	ID                    types.String `tfsdk:"id"`
	Cloud                 types.String `tfsdk:"cloud"`
	Name                  types.String `tfsdk:"name"`
	Status                types.String `tfsdk:"status"`
	FailureCode           types.String `tfsdk:"failure_code"`
	FailureMessage        types.String `tfsdk:"failure_message"`
	ExternalAccountID     types.String `tfsdk:"external_account_id"`
	Access                types.String `tfsdk:"access"`
	IssuerURL             types.String `tfsdk:"issuer_url"`
	Audience              types.String `tfsdk:"audience"`
	Subject               types.String `tfsdk:"subject"`
	QuickCreateURL        types.String `tfsdk:"quick_create_url"`
	SetupTemplate         types.String `tfsdk:"setup_template"`
	SetupTemplateFormat   types.String `tfsdk:"setup_template_format"`
	SetupFiles            types.Map    `tfsdk:"setup_files"`
	Permissions           types.List   `tfsdk:"permissions"`
	AgentImage            types.String `tfsdk:"agent_image"`
	AgentTokenVariables   types.List   `tfsdk:"agent_token_variables"`
	AgentTokenHowTo       types.String `tfsdk:"agent_token_how_to"`
	AgentEnrollCommand    types.String `tfsdk:"agent_enroll_command"`
	AgentRunCommand       types.String `tfsdk:"agent_run_command"`
	AgentContainerCommand types.String `tfsdk:"agent_container_command"`
	VerifiedAt            types.String `tfsdk:"verified_at"`
	CreatedAt             types.String `tfsdk:"created_at"`
}

// fromAPIResponse builds the state from a connection.
func fromAPIResponse(ctx context.Context, c *client.CloudConnection, diags *diag.Diagnostics) model {
	s := &c.Setup
	files := map[string]string{}
	for _, f := range s.Files {
		files[f.Name] = f.Content
	}
	m := model{
		ID:                  types.StringValue(c.ID),
		Cloud:               types.StringValue(c.Provider),
		Name:                types.StringValue(c.Name),
		Status:              types.StringValue(c.Status),
		FailureCode:         types.StringNull(),
		FailureMessage:      types.StringNull(),
		ExternalAccountID:   resourcekit.OptionalString(c.ExternalAccountID),
		Access:              resourcekit.OptionalString(&s.Access),
		IssuerURL:           resourcekit.OptionalString(s.IssuerURL),
		Audience:            resourcekit.OptionalString(s.Audience),
		Subject:             resourcekit.OptionalString(&s.Subject),
		QuickCreateURL:      resourcekit.OptionalString(s.QuickCreateURL),
		SetupTemplate:       resourcekit.OptionalString(&s.Template),
		SetupTemplateFormat: resourcekit.OptionalString(&s.TemplateFormat),
		VerifiedAt:          resourcekit.Timestamp(c.VerifiedAt),
		CreatedAt:           resourcekit.Timestamp(c.CreatedAt),
		AgentImage:          types.StringNull(), AgentTokenHowTo: types.StringNull(), AgentEnrollCommand: types.StringNull(),
		AgentRunCommand: types.StringNull(), AgentContainerCommand: types.StringNull(),
	}
	if m.ExternalAccountID.IsNull() {
		m.ExternalAccountID = resourcekit.OptionalString(c.AWSAccountID)
	}
	if c.Failure != nil {
		m.FailureCode, m.FailureMessage = types.StringValue(c.Failure.Code), types.StringValue(c.Failure.Message)
	}
	var d diag.Diagnostics
	m.SetupFiles, d = types.MapValueFrom(ctx, types.StringType, files)
	diags.Append(d...)
	m.Permissions, d = types.ListValueFrom(ctx, types.StringType, nonNil(s.Permissions))
	diags.Append(d...)
	var tokens []string
	if a := s.Agent; a != nil {
		m.AgentImage = resourcekit.OptionalString(&a.Image)
		m.AgentTokenHowTo = resourcekit.OptionalString(&a.TokenHowTo)
		m.AgentEnrollCommand = resourcekit.OptionalString(&a.EnrollCommand)
		m.AgentRunCommand = resourcekit.OptionalString(&a.RunCommand)
		m.AgentContainerCommand = resourcekit.OptionalString(&a.ContainerCommand)
		tokens = a.TokenVariables
	}
	m.AgentTokenVariables, d = types.ListValueFrom(ctx, types.StringType, nonNil(tokens))
	diags.Append(d...)
	return m
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
