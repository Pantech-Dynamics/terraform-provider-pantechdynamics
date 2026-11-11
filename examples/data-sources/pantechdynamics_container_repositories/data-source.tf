# Every repository in the namespace, including those created by a first push.
data "pantechdynamics_container_repositories" "all" {}

output "public_repositories" {
  value = [for r in data.pantechdynamics_container_repositories.all.repositories : r.full_name if r.visibility == "public"]
}
