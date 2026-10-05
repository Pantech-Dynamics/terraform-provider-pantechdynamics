# A slice of the network's address range. Instances attach to a subnet, and its
# firewall rules decide what traffic reaches them. The range must sit inside the
# network's.
resource "pantechdynamics_subnet" "web" {
  network_id = pantechdynamics_network.main.id
  name       = "web"
  cidr       = "10.0.1.0/24"
}
