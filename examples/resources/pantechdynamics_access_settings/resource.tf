# Members see nothing unless a policy allows it. Owners and admins are not
# affected. Declare this once per organization; it needs an owner's API key.
# Destroying it leaves the setting as it is.
resource "pantechdynamics_access_settings" "this" {
  member_baseline = "none"
}
