# Names in a Pantech DNS zone of your organization are validated by Pantech:
# create waits until the certificate is issued, usually within two minutes.
resource "pantechdynamics_dns_zone" "example" {
  name = "example.com"
}

resource "pantechdynamics_certificate" "site" {
  names = ["example.com", "*.example.com"]

  depends_on = [pantechdynamics_dns_zone.example]
}

# A name at another DNS host needs one CNAME, listed in validation_records.
# To add it from the same configuration, do not wait for issuance: the
# certificate is issued by itself once the CNAME is in place.
resource "pantechdynamics_certificate" "partner" {
  names             = ["api.example.org"]
  wait_for_issuance = false
}

output "cnames_to_add" {
  value = pantechdynamics_certificate.partner.validation_records
}
