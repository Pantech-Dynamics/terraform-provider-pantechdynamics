# Recommended (Terraform 1.10 and later): export a certificate created with
# exportable = true without the private key ever reaching the plan or state.
# Use the values only where ephemeral values are accepted, such as a
# write-only argument or a provider block. Every export emails your owners
# and admins, so each run that opens this sends one email.
resource "pantechdynamics_certificate" "edge" {
  names      = ["edge.example.com"]
  exportable = true
}

ephemeral "pantechdynamics_certificate_export" "edge" {
  certificate_id = pantechdynamics_certificate.edge.id
}

# Then pass ephemeral.pantechdynamics_certificate_export.edge.private_key_pem
# and .full_chain_pem to a write-only argument (one whose name usually ends
# in _wo) of the resource that installs them, for example your secret store's
# provider, so the key is used during the run and kept nowhere by Terraform.
