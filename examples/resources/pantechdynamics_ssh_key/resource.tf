# Register an existing public key.
resource "pantechdynamics_ssh_key" "laptop" {
  name       = "laptop"
  public_key = file("~/.ssh/id_ed25519.pub")
}

# Or let Pantech Dynamics generate a keypair. The private key is shown once and
# is kept only in Terraform state, so protect the state.
resource "pantechdynamics_ssh_key" "generated" {
  name = "ci-deploy"
}

output "ci_deploy_private_key" {
  value     = pantechdynamics_ssh_key.generated.private_key
  sensitive = true
}
