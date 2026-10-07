# Look an existing cluster up by name (or by id). Names are unique in the
# organization. The lookup exposes metadata only, never the kubeconfig.
data "pantechdynamics_kubernetes_cluster" "prod" {
  name = "prod"
}

output "prod_cluster" {
  value = {
    version  = data.pantechdynamics_kubernetes_cluster.prod.kubernetes_version
    workers  = data.pantechdynamics_kubernetes_cluster.prod.workers
    upgrades = data.pantechdynamics_kubernetes_cluster.prod.available_upgrade_ids
    # enabled, min_workers and max_workers (both 0 when autoscaling is off)
    autoscaling       = data.pantechdynamics_kubernetes_cluster.prod.autoscaling
    endpoint          = data.pantechdynamics_kubernetes_cluster.prod.endpoint
    api_allowed_cidrs = data.pantechdynamics_kubernetes_cluster.prod.api_allowed_cidrs
    volume_storage_gb = data.pantechdynamics_kubernetes_cluster.prod.volume_storage_gb
  }
}
