data "pantechdynamics_images" "all" {}

# Images that can be used in a given zone.
output "images_in_abuja_standard" {
  value = [for i in data.pantechdynamics_images.all.images : i.slug if contains(i.zones, "af-abj-1")]
}
