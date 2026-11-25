package cloudconnectionfederation

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// toUpdateRequest sends the one federation the configuration sets.
func toUpdateRequest(m model) client.UpdateCloudConnectionRequest {
	var req client.UpdateCloudConnectionRequest
	switch {
	case !m.RoleARN.IsNull() && !m.RoleARN.IsUnknown():
		arn := m.RoleARN.ValueString()
		req.RoleARN = &arn
	case m.GCP != nil:
		req.GCP = &client.CloudGCPFederation{ProjectNumber: m.GCP.ProjectNumber.ValueString(), PoolID: m.GCP.PoolID.ValueString(),
			ProviderID: m.GCP.ProviderID.ValueString(), ServiceAccountEmail: m.GCP.ServiceAccountEmail.ValueString(), ProjectIDs: valuesOf(m.GCP.ProjectIDs)}
	case m.Azure != nil:
		req.Azure = &client.CloudAzureFederation{TenantID: m.Azure.TenantID.ValueString(), ClientID: m.Azure.ClientID.ValueString(),
			SubscriptionIDs: valuesOf(m.Azure.SubscriptionIDs)}
	case m.Huawei != nil:
		h := &client.CloudHuaweiFederation{IdentityProviderID: m.Huawei.IdentityProviderID.ValueString(), Projects: []client.CloudHuaweiProject{}}
		for _, p := range m.Huawei.Projects {
			h.Projects = append(h.Projects, client.CloudHuaweiProject{Region: p.Region.ValueString(), ID: p.ID.ValueString()})
		}
		req.Huawei = h
	}
	return req
}

// withStatus copies the arguments and sets what the check found.
func withStatus(m model, c *client.CloudConnection) model {
	m.ID = types.StringValue(c.ID)
	m.ConnectionID = types.StringValue(c.ID)
	m.Status = types.StringValue(c.Status)
	m.ExternalAccountID = resourcekit.OptionalString(c.ExternalAccountID)
	if m.ExternalAccountID.IsNull() {
		m.ExternalAccountID = resourcekit.OptionalString(c.AWSAccountID)
	}
	m.VerifiedAt = resourcekit.Timestamp(c.VerifiedAt)
	return m
}

// fromFederation reads back the federation the connection holds, so a change
// made outside Terraform shows in the plan.
func fromFederation(m *model, c *client.CloudConnection) {
	switch {
	case c.RoleARN != nil && *c.RoleARN != "":
		m.RoleARN, m.GCP, m.Azure, m.Huawei = types.StringValue(*c.RoleARN), nil, nil, nil
	case c.GCP != nil:
		m.RoleARN, m.Azure, m.Huawei = types.StringNull(), nil, nil
		m.GCP = &gcpModel{ProjectNumber: types.StringValue(c.GCP.ProjectNumber), PoolID: types.StringValue(c.GCP.PoolID),
			ProviderID: types.StringValue(c.GCP.ProviderID), ServiceAccountEmail: types.StringValue(c.GCP.ServiceAccountEmail), ProjectIDs: values(c.GCP.ProjectIDs)}
	case c.Azure != nil:
		m.RoleARN, m.GCP, m.Huawei = types.StringNull(), nil, nil
		m.Azure = &azureModel{TenantID: types.StringValue(c.Azure.TenantID), ClientID: types.StringValue(c.Azure.ClientID), SubscriptionIDs: values(c.Azure.SubscriptionIDs)}
	case c.Huawei != nil:
		m.RoleARN, m.GCP, m.Azure = types.StringNull(), nil, nil
		h := &huaweiModel{IdentityProviderID: types.StringValue(c.Huawei.IdentityProviderID), Projects: []huaweiProjectModel{}}
		for _, p := range c.Huawei.Projects {
			h.Projects = append(h.Projects, huaweiProjectModel{Region: types.StringValue(p.Region), ID: types.StringValue(p.ID)})
		}
		m.Huawei = h
	}
}

func valuesOf(vs []types.String) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.ValueString())
	}
	return out
}

func values(ss []string) []types.String {
	out := make([]types.String, 0, len(ss))
	for _, s := range ss {
		out = append(out, types.StringValue(s))
	}
	return out
}
