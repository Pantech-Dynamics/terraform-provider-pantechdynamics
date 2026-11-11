# A team groups members, so policies are attached once for all of them.
resource "pantechdynamics_team" "platform" {
  name        = "Platform"
  description = "Runs staging and production"
}
