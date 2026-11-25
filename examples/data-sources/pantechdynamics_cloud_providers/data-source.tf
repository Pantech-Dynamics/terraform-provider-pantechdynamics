data "pantechdynamics_cloud_providers" "all" {}

# The clouds this platform can connect now, and how Pantech reads each.
output "available_clouds" {
  value = { for p in data.pantechdynamics_cloud_providers.all.providers : p.slug => p.access if p.available }
}
