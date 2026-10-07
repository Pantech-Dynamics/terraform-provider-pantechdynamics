# A cluster in a VPC subnet whose workers the cluster autoscaler keeps between
# 2 and 5, with its API server reachable only from an office range, and its
# kubeconfig written to a local file for kubectl.

resource "pantechdynamics_network" "main" {
  name = "main"
  cidr = "10.0.0.0/16"
}

# In a VPC zone the nodes go in a subnet. In a standard zone (af-abj-1) leave
# subnet_id out.
resource "pantechdynamics_subnet" "k8s" {
  network_id = pantechdynamics_network.main.id
  name       = "k8s"
  cidr       = "10.0.1.0/24"
}

# Firewall rules are stateless: the nodes need outbound traffic to pull images.
resource "pantechdynamics_firewall_rule" "k8s_egress" {
  subnet_id = pantechdynamics_subnet.k8s.id
  number    = 200
  direction = "egress"
  protocol  = "all"
  cidr      = "0.0.0.0/0"
}

# The versions offered in the network's zone, newest first.
data "pantechdynamics_kubernetes_versions" "zone" {
  zone_id = pantechdynamics_network.main.zone
}

locals {
  newest_version = [for v in data.pantechdynamics_kubernetes_versions.zone.versions : v if v.status == "available"][0]
}

resource "pantechdynamics_kubernetes_cluster" "prod" {
  name                  = "prod"
  zone_id               = pantechdynamics_network.main.zone
  subnet_id             = pantechdynamics_subnet.k8s.id
  kubernetes_version_id = local.newest_version.id

  # Every node, control and worker, is a VM of this plan and is billed as one.
  # It needs at least the version's min_cpu and min_memory_mb.
  node_plan = "s-2vcpu-4gb"

  # 1 (the default), or 3 where the zone is in ha_zone_ids. Changing it, the
  # name, zone, subnet or plan replaces the cluster.
  control_nodes = 1

  # The autoscaler adds and removes workers between min_workers and max_workers
  # as pods need them. The cluster starts at min_workers. Workers are billed as
  # VMs of node_plan while they run, and credit and account limits are checked
  # for max_workers. Changes in place. Leave workers out while autoscaling is
  # enabled: the autoscaler owns the count, which is reported in workers.
  autoscaling = {
    enabled     = true
    min_workers = 2
    max_workers = 5
  }

  # For a fixed count instead, remove autoscaling and set workers (1 to 10),
  # which changes in place (scale):
  # workers = 2

  # VPC zone only: the addresses allowed to reach the API server, at most 20
  # IPv4 CIDRs. Leave it out (or set []) to allow any address. Changes in place.
  api_allowed_cidrs = ["203.0.113.0/24"]

  # To upgrade, set kubernetes_version_id to one of available_upgrade_ids: the
  # next patch or minor version, never down.

  timeouts = {
    create = "60m"
    update = "60m"
    delete = "20m"
  }

  depends_on = [pantechdynamics_firewall_rule.k8s_egress]
}

# The kubeconfig is a cluster-admin credential. It is also in the Terraform
# state, so keep the state somewhere private.
resource "local_sensitive_file" "kubeconfig" {
  content         = pantechdynamics_kubernetes_cluster.prod.kube_config
  filename        = "${path.module}/kubeconfig-prod.yaml"
  file_permission = "0600"
}

output "kubernetes_version" {
  value = pantechdynamics_kubernetes_cluster.prod.kubernetes_version
}

# The API server's URL, and the storage persistent volume claims use (billed
# with the nodes' disks).
output "endpoint" {
  value = pantechdynamics_kubernetes_cluster.prod.endpoint
}

output "volume_storage_gb" {
  value = pantechdynamics_kubernetes_cluster.prod.volume_storage_gb
}

output "kubeconfig" {
  value     = pantechdynamics_kubernetes_cluster.prod.kube_config
  sensitive = true
}
