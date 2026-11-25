# A connection to an AWS account. Its setup_template is a CloudFormation
# template for a read-only role only Pantech can assume; create the stack
# (here with the AWS provider), then send its RoleArn output with
# pantechdynamics_cloud_connection_federation.
resource "pantechdynamics_cloud_connection" "aws" {
  name = "Production AWS"
}

resource "aws_cloudformation_stack" "pantech_migrate" {
  name          = "pantech-migrate"
  template_body = pantechdynamics_cloud_connection.aws.setup_template
  capabilities  = ["CAPABILITY_NAMED_IAM"]
}

# A Google Cloud connection: run setup_template (a gcloud script), then send
# what it printed with pantechdynamics_cloud_connection_federation.
resource "pantechdynamics_cloud_connection" "gcp" {
  cloud = "gcp"
  name  = "Production GCP"
}

# A Hetzner connection, read by the Pantech Migrate agent you run with a
# read-only token that never leaves your machine. Enroll the agent with
# `pantech migrate agent enroll <id>`.
resource "pantechdynamics_cloud_connection" "hetzner" {
  cloud = "hetzner"
  name  = "Hetzner"
}

output "hetzner_token_how_to" {
  value = pantechdynamics_cloud_connection.hetzner.agent_token_how_to
}
