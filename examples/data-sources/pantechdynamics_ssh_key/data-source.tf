# Look a registered SSH key up by name (or by id), to install it on instances
# without managing it.
data "pantechdynamics_ssh_key" "laptop" {
  name = "laptop"
}

output "laptop_fingerprint" {
  value = data.pantechdynamics_ssh_key.laptop.fingerprint
}
