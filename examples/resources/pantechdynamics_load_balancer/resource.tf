# Spreads HTTP on one public address across two instances in a VPC subnet.

resource "pantechdynamics_network" "main" {
  name = "main"
  cidr = "10.0.0.0/16"
}

resource "pantechdynamics_subnet" "web" {
  network_id = pantechdynamics_network.main.id
  name       = "web"
  cidr       = "10.0.1.0/24"
}

# Two targets in the subnet. Use a plan from the "vpc" placement.
resource "pantechdynamics_instance" "web" {
  count      = 2
  name       = "web-${count.index + 1}"
  plan_slug  = "individual"
  image_slug = "ubuntu-24-04"
  subnet_id  = pantechdynamics_subnet.web.id
}

# An address that carries load balancers, one per public port. It is billed as
# a public IP; the load balancer itself has no fee.
resource "pantechdynamics_public_ip" "lb" {
  network_id = pantechdynamics_network.main.id
  purpose    = "load_balancer"
}

# A load balancer opens nothing on its own: the subnet's firewall must allow the
# private port the targets listen on.
resource "pantechdynamics_firewall_rule" "http" {
  subnet_id  = pantechdynamics_subnet.web.id
  number     = 100
  protocol   = "tcp"
  port_start = 8080
  cidr       = "0.0.0.0/0"
}

resource "pantechdynamics_load_balancer" "web" {
  name         = "web"
  public_ip_id = pantechdynamics_public_ip.lb.id
  subnet_id    = pantechdynamics_subnet.web.id
  public_port  = 80
  private_port = 8080        # defaults to public_port
  algorithm    = "leastconn" # roundrobin (default), leastconn or source

  # Targets change in place. The set is the whole list.
  instance_ids = pantechdynamics_instance.web[*].id

  # Only these sources may connect. Omit for any source. Changing it, the
  # ports, the public IP or the subnet replaces the load balancer.
  # cidr_list = ["203.0.113.0/24"]
}

output "web_url" {
  value = "http://${pantechdynamics_load_balancer.web.public_ip_address}"
}
