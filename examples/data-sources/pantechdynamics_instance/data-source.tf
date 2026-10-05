# Look an existing instance up by id (or by name). Instance names are not unique:
# if several share the name, the lookup fails and lists their ids.
data "pantechdynamics_instance" "app" {
  name = "app-1"
}

output "app_private_address" {
  value = data.pantechdynamics_instance.app.private_ipv4
}
