# Look an existing load balancer up by name (or by id). Names are not unique:
# if several share the name, the lookup fails and lists their ids.
data "pantechdynamics_load_balancer" "web" {
  name = "web"
}

output "web_address" {
  value = "${data.pantechdynamics_load_balancer.web.public_ip_address}:${data.pantechdynamics_load_balancer.web.public_port}"
}
