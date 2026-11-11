# A hosted zone on Pantech DNS. Create waits until the zone has its name
# servers; set them at your domain's registrar to make the zone active.
resource "pantechdynamics_dns_zone" "example" {
  name        = "example.com"
  description = "Marketing site"
}

output "name_servers" {
  value = pantechdynamics_dns_zone.example.name_servers
}
