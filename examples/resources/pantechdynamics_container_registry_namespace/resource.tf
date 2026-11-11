# Your organization's one namespace in the container registry. Images are
# <registry_host>/<name>/<repository>:<tag>. The name can change only while the
# namespace holds no repository, and a namespace cannot be deleted:
# `terraform destroy` only removes it from state.
resource "pantechdynamics_container_registry_namespace" "main" {
  name           = "acme"
  create_on_push = false # require repositories to be created first
}

output "registry_host" {
  value = pantechdynamics_container_registry_namespace.main.registry_host
}
