# Send instance and volume events, and paid invoices, to your own endpoint.
# Every delivery is a JSON POST signed with the webhook's secret.
resource "pantechdynamics_webhook" "ops" {
  url         = "https://hooks.example.com/pantech"
  description = "Production alerts"
  events = [
    "instance.*",           # every instance event
    "volume.attach_failed", # one exact event type
    "invoice.paid",
  ]
}

# Subscribe to everything, but keep it paused for now.
resource "pantechdynamics_webhook" "audit" {
  url     = "https://audit.example.com/pantech"
  events  = ["*"]
  enabled = false
}

# The signing secret is shown only when the webhook is created, so it lives in
# Terraform state. Hand it to the receiver, for example through a secret store.
# Rotating it is done in the console or the CLI, not by Terraform.
output "ops_webhook_secret" {
  value     = pantechdynamics_webhook.ops.secret
  sensitive = true
}
