# Look an existing zone up by name (or by id), to add records to it.
data "pantechdynamics_dns_zone" "example" {
  name = "example.com"
}

resource "pantechdynamics_dns_record" "api" {
  zone_id = data.pantechdynamics_dns_zone.example.id
  name    = "api"
  type    = "CNAME"
  values  = ["lb.example.net"]
}
