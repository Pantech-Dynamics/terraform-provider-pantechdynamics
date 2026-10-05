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
  name = "web-1"

  # Changing the plan to a bigger one resizes the instance in place. It is stopped
  # and restarted, which takes several minutes, and the disk grows. A smaller plan
  # is refused: use `terraform apply -replace` for that, which destroys the disk.
  plan_slug = "individual"

  image_slug = "ubuntu-24-04"

  ssh_key_id = pantechdynamics_ssh_key.admin.id

  # Changing the security group updates the instance in place. The platform only
  # accepts it on a stopped instance, so the instance is stopped, switched, and
  # started again if it should be running.
  security_group_id = pantechdynamics_security_group.web.id

  tags = {
    purpose = "web"
  }

  # "running" (the default) or "stopped". Changing it starts or stops the
  # instance in place. A stopped instance is billed for storage only.
  desired_state = "running"

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

# An instance in a VPC subnet instead. It has only a private address, so attach a
# pantechdynamics_public_ip to reach it from outside, and it cannot use a security
# group: the subnet's firewall rules apply. Use a plan from the "vpc" placement.
# resource "pantechdynamics_instance" "private" {
#   name       = "private-1"
#   plan_slug  = "individual"
#   image_slug = "ubuntu-24-04"
#   ssh_key_id = pantechdynamics_ssh_key.admin.id
#   subnet_id  = pantechdynamics_subnet.web.id
# }

# A standard instance on the zone's private database network, so it reaches
# managed databases by their private address. Its security group must let in
# nothing from the private network's range (the standard placement's
# private_network_cidr in pantechdynamics_region, 10.250.0.0/20 in af-abj-1): a group
# applies to every interface, so 0.0.0.0/0 rules, ICMP ones too, are refused with
# SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK, and the error names the rules to narrow.
resource "pantechdynamics_security_group" "app" {
  name = "app-tier"
  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "203.0.113.0/24" },
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "198.51.100.0/24" },
    { direction = "egress", protocol = "all", cidr = "0.0.0.0/0" },
  ]
}

resource "pantechdynamics_instance" "app" {
  name              = "app-1"
  plan_slug         = "individual"
  image_slug        = "ubuntu-24-04"
  ssh_key_id        = pantechdynamics_ssh_key.admin.id
  security_group_id = pantechdynamics_security_group.app.id

  # Attached and detached in place, without a restart. Inside the guest, add the
  # new interface to netplan with dhcp4: true and
  # dhcp4-overrides { use-routes: false, use-dns: false }, then netplan apply.
  private_network = true
}

# Allow this address on a database as a /32 access rule.
output "app_private_network_ip" {
  value = pantechdynamics_instance.app.private_network_ip
}
