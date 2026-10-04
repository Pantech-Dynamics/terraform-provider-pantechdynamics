# Look one region up by code, and see where each kind of instance lands there.
data "pantechdynamics_region" "abuja" {
  code = "af-abj"
}

output "abuja_placements" {
  value = data.pantechdynamics_region.abuja.placements
}
