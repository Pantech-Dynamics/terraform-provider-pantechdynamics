# Look one image up by slug, and check it can be used in the zone you create in.
data "pantechdynamics_image" "ubuntu" {
  slug = "ubuntu-24-04"
}

output "ubuntu_zones" {
  value = data.pantechdynamics_image.ubuntu.zones
}
