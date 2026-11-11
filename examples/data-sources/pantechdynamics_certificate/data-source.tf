# Look a certificate up by one of its names (or by id).
data "pantechdynamics_certificate" "site" {
  name = "*.example.com"
}

output "expires" {
  value = data.pantechdynamics_certificate.site.not_after
}
