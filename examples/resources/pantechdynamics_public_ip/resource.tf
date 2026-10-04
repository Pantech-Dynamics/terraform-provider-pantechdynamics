# A public address that maps every port to one instance (static NAT), billed
# monthly. It opens nothing on its own: the subnet's firewall rules must allow
# the traffic too.
resource "pantechdynamics_public_ip" "web" {
  network_id  = pantechdynamics_network.main.id
  instance_id = pantechdynamics_instance.web.id
}

output "web_address" {
  value = pantechdynamics_public_ip.web.address
}

# An address that carries port forwarding rules instead takes no instance.
resource "pantechdynamics_public_ip" "gateway" {
  network_id = pantechdynamics_network.main.id
  purpose    = "port_forwarding"
}
