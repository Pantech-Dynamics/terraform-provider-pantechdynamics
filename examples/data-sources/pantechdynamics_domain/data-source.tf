# Look a domain of your organization up by name (or by id).
data "pantechdynamics_domain" "example" {
  name = "example.com"
}

output "expires_at" {
  value = data.pantechdynamics_domain.example.expires_at
}
