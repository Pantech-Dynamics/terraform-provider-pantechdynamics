# A push token for CI, limited to one repository. Its secret is shown only
# when the token is created, so it is kept in Terraform state: treat the state
# as a secret. Tokens cannot change: changing any argument issues a new token.
resource "pantechdynamics_container_registry_token" "ci" {
  name         = "github-actions"
  permission   = "push"
  repositories = ["api/server"]
  expires_at   = "2027-11-10T00:00:00Z"
}

# A pull token for a cluster, for every repository in the namespace.
resource "pantechdynamics_container_registry_token" "cluster" {
  name       = "prod-cluster-pull"
  permission = "pull"
}

# docker login <registry_host> -u pantech -p <secret>
output "ci_registry_password" {
  value     = pantechdynamics_container_registry_token.ci.secret
  sensitive = true
}
