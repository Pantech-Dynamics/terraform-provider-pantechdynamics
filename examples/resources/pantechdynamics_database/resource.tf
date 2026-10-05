# Creating a database places an order that is paid first, from account credit or
# the default card, like an instance of the same plan, so it costs money.
# password_wo is write-only (Terraform 1.11 or later): it is sent to the API and
# never stored in plan or state.
data "pantechdynamics_database_engines" "all" {}

variable "db_password" {
  description = "Admin password: 16 to 128 printable ASCII characters, no spaces, quotes or backslashes."
  type        = string
  sensitive   = true
  ephemeral   = true
}

resource "pantechdynamics_database" "orders" {
  name      = "orders"
  engine    = "postgresql"
  version   = "18"
  plan_slug = "starter"

  # In a VPC zone the database goes in a subnet, and its default access rule is
  # the VPC's CIDR. In a standard zone (af-abj-1) omit subnet_id.
  zone_id   = "af-abj-2"
  subnet_id = pantechdynamics_subnet.data.id

  # Defaults to "dbadmin". Changing it replaces the database.
  admin_username = "app"

  # To change the password, change password_wo and increase password_wo_version
  # together: Terraform cannot see a change to a write-only value on its own.
  password_wo         = var.db_password
  password_wo_version = 1

  # The data disk in GB. Omit it for the plan's disk_gb. Increasing it grows the
  # disk in place, online; it can never shrink: a smaller value is an error at
  # plan time, and the database is never replaced for it. At most 2000 GB, and
  # above the plan's size a multiple of 10 GB.
  storage_gb = 40

  # The whole allow-list, replaced in place on change. Omit it to keep the zone's
  # default. 0.0.0.0/0 and prefixes wider than /8 are refused.
  # An instance on the private database network connects from its
  # private_network_ip; allow it as a /32.
  access_rules = ["10.0.0.0/16", "203.0.113.10/32", "${pantechdynamics_instance.app.private_network_ip}/32"]

  # Security groups add the CIDRs of their ingress rules that cover the engine's
  # port. Other rules are listed in ignored_security_group_rules. At most 5.
  # security_group_ids = [data.pantechdynamics_security_group.office.id]

  # "running" (the default) or "stopped".
  desired_state = "running"

  timeouts = {
    create = "45m"
    update = "20m"
    delete = "30m"
  }
}

output "orders_connection" {
  value = "postgresql://${pantechdynamics_database.orders.admin_username}@${pantechdynamics_database.orders.hostname}:${pantechdynamics_database.orders.port}/postgres"
}

# With the hashicorp/random provider 3.7 or later, an ephemeral password never
# touches state either. Keep it somewhere you can read it back, such as a secret
# manager, because the API never returns it.
# ephemeral "random_password" "db" {
#   length           = 32
#   override_special = "-_.~!#$%^&*()+=[]{}<>:;,?/|"
# }
