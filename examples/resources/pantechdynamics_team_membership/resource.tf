resource "pantechdynamics_team" "platform" {
  name = "Platform"
}

# One resource per membership. The member must already be in the
# organization; destroying this takes them out of the team only.
resource "pantechdynamics_team_membership" "ada" {
  team_id = pantechdynamics_team.platform.id
  user_id = "usr_06ggf8abcdefghjkmnpqrstvw"
}
