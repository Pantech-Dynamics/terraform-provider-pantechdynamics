# Registering a domain charges your organization's credit and cannot be undone.
# `terraform destroy` does NOT release the domain: it only removes it from
# state. Turn auto_renew off to let a domain you no longer want expire.
resource "pantechdynamics_domain" "example" {
  name         = "example.com"
  years        = 1
  accept_terms = true

  contact = {
    first_name    = "Ada"
    last_name     = "Obi"
    email         = "ada@example.com"
    phone         = "+234.8012345678"
    address_line1 = "1 Marina"
    city          = "Lagos"
    state         = "Lagos"
    country_code  = "NG"
    postal_code   = "101001"
  }

  # With name_servers unset the domain uses Pantech DNS: serve its records
  # from a zone with the same name.
  depends_on = [pantechdynamics_dns_zone.example]
}

resource "pantechdynamics_dns_zone" "example" {
  name = "example.com"
}

# Or delegate to other name servers instead:
#
#   name_servers = ["ns1.example.net", "ns2.example.net"]
