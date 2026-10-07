# Kubernetes versions offered for new clusters in one zone, newest first.
data "pantechdynamics_kubernetes_versions" "abj2" {
  zone_id = "af-abj-2"
}

output "available_versions" {
  value = {
    for v in data.pantechdynamics_kubernetes_versions.abj2.versions : v.version => v.id if v.status == "available"
  }
}

# Whether this zone offers a highly available control plane (control_nodes = 3).
output "ha_available" {
  value = contains(data.pantechdynamics_kubernetes_versions.abj2.ha_zone_ids, "af-abj-2")
}
