data "pantechdynamics_disk_offerings" "all" {}

# Offerings whose volumes can be attached on staging.
output "local_offerings" {
  value = [for o in data.pantechdynamics_disk_offerings.all.disk_offerings : o.slug if o.storage_type == "local"]
}

# Offerings priced by the gigabyte, which take a size_gb.
output "customized_offerings" {
  value = [for o in data.pantechdynamics_disk_offerings.all.disk_offerings : o.slug if o.custom_size]
}
