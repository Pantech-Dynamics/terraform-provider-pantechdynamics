# Look an existing VPC network up by name (or by id). Network names are not
# unique: if several share the name, the lookup fails and lists their ids.
data "pantechdynamics_network" "main" {
  name = "main"
}

output "main_cidr" {
  value = data.pantechdynamics_network.main.cidr
}
