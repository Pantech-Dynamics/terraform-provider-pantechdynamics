# The connections are pantechdynamics_cloud_connection resources, as in its
# example (cloud = "aws", "gcp" and "azure").

# AWS: the RoleArn output of the stack made from the connection's template.
# Applying waits until Pantech has verified the role.
resource "pantechdynamics_cloud_connection_federation" "aws" {
  connection_id = pantechdynamics_cloud_connection.aws.id
  role_arn      = aws_cloudformation_stack.pantech_migrate.outputs["RoleArn"]
}

# Google Cloud: what the connection's gcloud script printed.
resource "pantechdynamics_cloud_connection_federation" "gcp" {
  connection_id = pantechdynamics_cloud_connection.gcp.id
  gcp = {
    project_number        = "123456789012"
    pool_id               = "pantech-migrate"
    provider_id           = "ccn-06gk3x9tq7m2v8r4y1c5d0zp8w"
    service_account_email = "pantech-migrate@web-prod.iam.gserviceaccount.com"
    project_ids           = ["web-prod", "data-prod"]
  }
}

# Azure: what the connection's Azure CLI script printed.
resource "pantechdynamics_cloud_connection_federation" "azure" {
  connection_id = pantechdynamics_cloud_connection.azure.id
  azure = {
    tenant_id        = "00000000-0000-0000-0000-000000000000"
    client_id        = "11111111-1111-1111-1111-111111111111"
    subscription_ids = ["22222222-2222-2222-2222-222222222222"]
  }
}
