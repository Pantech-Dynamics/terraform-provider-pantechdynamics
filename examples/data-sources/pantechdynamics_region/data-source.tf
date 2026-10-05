# Look one region up by code, and see where each kind of instance lands there.
data "pantechdynamics_region" "abuja" {
  code = "af-abj"
}

output "abuja_placements" {
  value = data.pantechdynamics_region.abuja.placements
}

# The private database network range to keep out of a security group, for an
# instance with private_network = true.
output "abuja_private_network_cidr" {
  value = one([for p in data.pantechdynamics_region.abuja.placements : p.private_network_cidr if p.kind == "standard"])
}
