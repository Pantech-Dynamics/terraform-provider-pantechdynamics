# The TLDs Pantech sells, with yearly prices in minor units.
data "pantechdynamics_domain_tlds" "all" {}

locals {
  com = one([for t in data.pantechdynamics_domain_tlds.all.tlds : t if t.tld == "com"])
}

# Registering example.com for two years costs register + one renewal year.
output "com_two_years_minor" {
  value = local.com.register_minor + local.com.renew_minor
}
