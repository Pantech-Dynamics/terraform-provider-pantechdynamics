# Look one image up by slug, and check it can be used in the zone you create in.
data "pantechdynamics_image" "ubuntu" {
  slug = "ubuntu-24-04"
}

output "ubuntu_zones" {
  value = data.pantechdynamics_image.ubuntu.zones
}

# The encrypted variant of Ubuntu 24.04. The lookup fails when encrypted VMs are
# not enabled for your organization, before anything is ordered.
data "pantechdynamics_image" "ubuntu_encrypted" {
  slug = "ubuntu-24-04-encrypted"
}

output "ubuntu_encrypted_zones" {
  value = data.pantechdynamics_image.ubuntu_encrypted.zones
}
