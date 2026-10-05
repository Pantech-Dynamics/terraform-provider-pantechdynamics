# Forwards public port 2222 on the gateway address to SSH (port 22) on one
# instance. The subnet's firewall must also allow port 22.
resource "pantechdynamics_port_forwarding_rule" "ssh" {
  public_ip_id       = pantechdynamics_public_ip.gateway.id
  instance_id        = pantechdynamics_instance.web.id
  public_port_start  = 2222
  private_port_start = 22
  # protocol = "tcp" is the default; ends default to the start, giving one port.
}
