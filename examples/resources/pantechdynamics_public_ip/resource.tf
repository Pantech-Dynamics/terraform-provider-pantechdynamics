# A public address that maps every port to one instance (static NAT), billed
# monthly while you hold it. It opens nothing on its own: the subnet's firewall
# rules must allow the traffic too.
#
# instance_id changes in place: point it at another instance to move the same
# address there (for example to a replacement server), or remove it to detach
# the address and keep it. A detached address is still yours and still billed,
# once, until it is attached again or destroyed.
resource "pantechdynamics_public_ip" "web" {
  network_id  = pantechdynamics_network.main.id
  instance_id = pantechdynamics_instance.web.id
}

output "web_address" {
  value = pantechdynamics_public_ip.web.address
}

# A static NAT address reserved without an instance, to attach later by
# setting instance_id.
resource "pantechdynamics_public_ip" "spare" {
  network_id = pantechdynamics_network.main.id
}

# An address that carries port forwarding rules instead takes no instance.
resource "pantechdynamics_public_ip" "gateway" {
  network_id = pantechdynamics_network.main.id
  purpose    = "port_forwarding"
}

# An address that carries load balancers (pantechdynamics_load_balancer), one per
# public port, also takes no instance.
resource "pantechdynamics_public_ip" "lb" {
  network_id = pantechdynamics_network.main.id
  purpose    = "load_balancer"
}
