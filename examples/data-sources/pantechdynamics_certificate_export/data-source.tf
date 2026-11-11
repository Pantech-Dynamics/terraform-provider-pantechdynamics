# For Terraform older than 1.10 only: prefer the
# pantechdynamics_certificate_export ephemeral resource, which never stores
# the key. With this data source the private key is kept in Terraform state,
# and every plan and refresh exports again, which emails your owners and
# admins each time: keep it in a configuration you apply rarely.
resource "pantechdynamics_certificate" "edge" {
  names      = ["edge.example.com"]
  exportable = true
}

data "pantechdynamics_certificate_export" "edge" {
  certificate_id = pantechdynamics_certificate.edge.id
}

resource "local_sensitive_file" "key" {
  filename = "${path.module}/edge.key"
  content  = data.pantechdynamics_certificate_export.edge.private_key_pem
}

resource "local_file" "fullchain" {
  filename = "${path.module}/edge.fullchain.pem"
  content  = data.pantechdynamics_certificate_export.edge.full_chain_pem
}
