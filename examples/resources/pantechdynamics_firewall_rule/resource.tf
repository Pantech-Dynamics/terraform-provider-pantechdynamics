# Rules are stateless and evaluated by number, lowest first (100 to 9999). A rule
# cannot be edited, so changing any argument replaces it.
resource "pantechdynamics_firewall_rule" "https" {
  subnet_id  = pantechdynamics_subnet.web.id
  number     = 100
  protocol   = "tcp"
  port_start = 443 # port_end defaults to port_start
  cidr       = "0.0.0.0/0"
}

# A range of ports, from one office network only.
resource "pantechdynamics_firewall_rule" "app" {
  subnet_id  = pantechdynamics_subnet.web.id
  number     = 110
  protocol   = "tcp"
  port_start = 8000
  port_end   = 8080
  cidr       = "203.0.113.0/24"
}

# Ping from anywhere. Omit icmp_type and icmp_code to match any.
resource "pantechdynamics_firewall_rule" "ping" {
  subnet_id = pantechdynamics_subnet.web.id
  number    = 120
  protocol  = "icmp"
  cidr      = "0.0.0.0/0"
}

# Because rules are stateless, outbound traffic needs its own rule.
resource "pantechdynamics_firewall_rule" "egress" {
  subnet_id = pantechdynamics_subnet.web.id
  number    = 200
  direction = "egress"
  protocol  = "all"
  cidr      = "0.0.0.0/0"
}
