# A private network (VPC), billed monthly. Subnets live inside it. A network has
# no in-place changes, so changing any argument replaces it.
resource "pantechdynamics_network" "main" {
  name = "main"
  cidr = "10.0.0.0/16"

  # region = "af-abj" # Omit for the account's default region.
}
