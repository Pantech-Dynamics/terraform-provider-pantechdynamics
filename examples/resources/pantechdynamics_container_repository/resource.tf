resource "pantechdynamics_container_registry_namespace" "main" {
  name = "acme"
}

# A private repository whose release tags can never be moved, keeping the 50
# newest tags (and every v* tag) and removing untagged images after a week.
# Destroying it deletes every image in it.
resource "pantechdynamics_container_repository" "api" {
  name           = "api/server"
  description    = "API server images"
  immutable_tags = true

  retention = {
    keep_last           = 50
    untagged_after_days = 7
    keep_pattern        = "v*"
  }

  # The repository needs the namespace, which no attribute references.
  depends_on = [pantechdynamics_container_registry_namespace.main]
}

# Push with: docker push <full_name>:<tag>
output "api_image" {
  value = pantechdynamics_container_repository.api.full_name
}
