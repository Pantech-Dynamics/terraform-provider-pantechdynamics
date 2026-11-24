# Read the Dynamic IP status, current outbound address, and recent rotation
# events for an instance.
data "pantechdynamics_dynamic_ip" "worker" {
  instance_id = pantechdynamics_instance.worker.id
}

output "current_outbound_address" {
  value = data.pantechdynamics_dynamic_ip.worker.current_address
}

output "recent_rotations" {
  value = data.pantechdynamics_dynamic_ip.worker.events
}
