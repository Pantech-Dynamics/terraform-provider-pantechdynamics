data "pantechdynamics_images" "all" {}

# Images that can be used in a given zone.
output "images_in_abuja_standard" {
  value = [for i in data.pantechdynamics_images.all.images : i.slug if contains(i.zones, "af-abj-1")]
}

# Encrypted images, and the plain image each one is the encrypted variant of.
# They are listed only once encrypted VMs are enabled for your organization.
output "encrypted_images" {
  value = { for i in data.pantechdynamics_images.all.images : i.slug => i.encrypted_variant_of if i.encrypted }
}
