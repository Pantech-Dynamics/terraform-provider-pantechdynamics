# Recipe: staging operators. Members of the team may restart and resize
# staging machines, from the office network only, and may never delete a
# production one (a matching deny always wins, even for admins).
resource "pantechdynamics_team" "staging_operators" {
  name = "Staging operators"
}

resource "pantechdynamics_access_policy" "staging_operators" {
  name        = "staging-operators"
  description = "Restart and resize staging machines"

  statement {
    sid       = "RestartStaging"
    effect    = "allow"
    actions   = ["instances:Read", "instances:Start", "instances:Stop", "instances:Reboot", "instances:Resize"]
    resources = ["name:staging-*"]

    conditions = {
      source_ip = ["102.89.0.0/16"]
    }
  }

  statement {
    sid       = "NoProdDeletes"
    effect    = "deny"
    actions   = ["instances:Delete"]
    resources = ["name:prod-*"]
  }
}

resource "pantechdynamics_access_policy_attachment" "staging_operators" {
  policy_id      = pantechdynamics_access_policy.staging_operators.id
  principal_type = "team"
  principal_id   = pantechdynamics_team.staging_operators.id
}

# The same kind of policy from JSON instead of blocks (set one or the other):
resource "pantechdynamics_access_policy" "nothing_without_mfa" {
  name = "no-deletes-without-mfa"
  policy_json = jsonencode({
    statements = [{
      sid       = "DenyDeletesWithoutMFA"
      effect    = "deny"
      actions   = ["*:Delete"]
      resources = ["*"]
      conditions = {
        mfa = false # on a deny: applies when the request lacks MFA
      }
    }]
  })
}
