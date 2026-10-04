# Creating an instance places an order that is paid from account credit or the
# default card, so it costs money. Look up valid plan and image slugs first.
data "pantechdynamics_plans" "all" {}
data "pantechdynamics_images" "all" {}

resource "pantechdynamics_ssh_key" "admin" {
  name       = "admin"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "pantechdynamics_security_group" "web" {
  name = "web-tier"
  rules = [
    { direction = "ingress", protocol = "icmp", cidr = "0.0.0.0/0" },
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "203.0.113.0/24" },
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "0.0.0.0/0" },
    { direction = "egress", protocol = "all", cidr = "0.0.0.0/0" },
  ]
}

resource "pantechdynamics_instance" "web" {
  # The name becomes the hostname. Keep it unique: the API accepts a duplicate
  # name and then fails the order, so the provider refuses it up front.
  name       = "web-1"
  plan_slug  = "individual"
  image_slug = "ubuntu-24-04"

  ssh_key_id        = pantechdynamics_ssh_key.admin.id
  security_group_id = pantechdynamics_security_group.web.id

  tags = {
    purpose = "web"
  }

  # Provisioning usually takes under a minute, deleting a few minutes.
  timeouts = {
    create = "30m"
    update = "10m"
    delete = "30m"
  }
}

output "web_address" {
  value = pantechdynamics_instance.web.private_ipv4
}
