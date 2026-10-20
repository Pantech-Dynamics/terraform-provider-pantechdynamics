# The platform installs the add-on and keeps it applied; status shows what
# the cluster runs.

# CPU and memory metrics, for kubectl top and the autoscalers. Destroying the
# resource turns the add-on off.
resource "pantechdynamics_kubernetes_addon" "metrics" {
  cluster_id = pantechdynamics_kubernetes_cluster.prod.id
  name       = "metrics-server" # or csi-driver, ingress-controller
}
