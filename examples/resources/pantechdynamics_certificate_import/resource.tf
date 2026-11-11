# Bring your own certificate for Pantech services to use. The private key is
# kept in Terraform state: treat the state as a secret. Pantech never renews
# an imported certificate: change the files (same names) and apply to
# replace it in place.
resource "pantechdynamics_certificate_import" "shop" {
  certificate_pem = file("${path.module}/shop.example.org.crt")
  chain_pem       = file("${path.module}/intermediates.pem")
  private_key_pem = file("${path.module}/shop.example.org.key")
  description     = "Bought from our CA"
}
