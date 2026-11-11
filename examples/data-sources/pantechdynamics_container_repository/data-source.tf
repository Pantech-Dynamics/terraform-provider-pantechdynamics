# Look a repository up by name (or by id), for example to reference its image.
data "pantechdynamics_container_repository" "api" {
  name = "api/server"
}

output "api_image" {
  value = "${data.pantechdynamics_container_repository.api.full_name}:v1.4.2"
}
