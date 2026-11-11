resource "pantechdynamics_team" "developers" {
  name = "Developers"
}

# A Pantech-managed policy attaches like your own.
data "pantechdynamics_access_policy" "developer" {
  id = "pol_pantech_developer"
}

resource "pantechdynamics_access_policy_attachment" "developers" {
  policy_id      = data.pantechdynamics_access_policy.developer.id
  principal_type = "team"
  principal_id   = pantechdynamics_team.developers.id
}

# Or straight to one member.
resource "pantechdynamics_access_policy_attachment" "auditor" {
  policy_id      = "pol_pantech_read_only"
  principal_type = "member"
  principal_id   = "usr_06ggf8abcdefghjkmnpqrstvw"
}
