# Look an existing security group up by name (or by id), for example the
# account's default group, without managing it.
data "pantechdynamics_security_group" "default" {
  name = "default"
}

output "default_group_id" {
  value = data.pantechdynamics_security_group.default.id
}
