# Workers of their own plan; each is billed as a VM of that plan once it has
# joined the cluster (observed_workers).

# Bigger workers only database pods may use: they must tolerate the taint and
# can select the nodes by the label.
resource "pantechdynamics_kubernetes_node_pool" "db" {
  cluster_id = pantechdynamics_kubernetes_cluster.prod.id
  name       = "db"
  node_plan  = "s-4vcpu-8gb" # each worker is billed as a VM of this plan
  workers    = 2             # changes in place

  labels = { role = "db" }
  taints = [{ key = "dedicated", value = "db", effect = "NoSchedule" }]
}

# Workers the autoscaler keeps between 1 and 5, starting at 1.
resource "pantechdynamics_kubernetes_node_pool" "burst" {
  cluster_id = pantechdynamics_kubernetes_cluster.prod.id
  name       = "burst"
  node_plan  = "s-2vcpu-4gb"
  autoscaling = {
    enabled     = true
    min_workers = 1
    max_workers = 5
  }
}
