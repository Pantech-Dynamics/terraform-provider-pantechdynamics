# A reusable firewall for standard instances. Inbound traffic that no rule
# allows is dropped. The rules are replaced as a whole, so list every rule you
# want, and note that a change affects every instance that uses the group.
resource "pantechdynamics_security_group" "web" {
  name = "web-tier"

  rules = [
    # Omit port_range to mean all ports, and for icmp.
    { direction = "ingress", protocol = "icmp", cidr = "0.0.0.0/0" },
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "0.0.0.0/0" },
    { direction = "ingress", protocol = "tcp", port_range = "80", cidr = "0.0.0.0/0" },

    # Limit SSH to an office network, using a range of ports for another service.
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "203.0.113.0/24" },
    { direction = "ingress", protocol = "tcp", port_range = "8000-8080", cidr = "10.0.0.0/16" },

    { direction = "egress", protocol = "all", cidr = "0.0.0.0/0" },
  ]

  timeouts = {
    create = "10m"
    update = "10m"
    delete = "10m"
  }
}
