# Enable Dynamic IP on a running standard instance. Outbound traffic leaves from
# a rotating public IPv4 address while inbound traffic continues to reach the
# instance's static address.
resource "pantechdynamics_dynamic_ip" "worker" {
  instance_id = pantechdynamics_instance.worker.id
  schedule    = "daily" # every_12_hours, daily, weekly, or monthly
  connections = "keep"  # keep (default) or move
  on_demand   = false   # requires on-demand rotation enabled for your organization
}

output "worker_outbound_ip" {
  value = pantechdynamics_dynamic_ip.worker.current_address
}
