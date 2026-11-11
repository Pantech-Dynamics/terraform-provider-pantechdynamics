# Recipe: a CI identity for one repository. CI runs as a dedicated member
# (invite a "ci-bot" user) whose API key may push and pull one container
# repository and change the records of one DNS zone, and nothing else.
# Members get read access to everything by default; with
# pantechdynamics_access_settings member_baseline = "none" they get only this.
# For pushing images alone, a pantechdynamics_container_registry_token scoped
# to the repository is simpler.
resource "pantechdynamics_container_repository" "api" {
  name = "api/server"
}

resource "pantechdynamics_dns_zone" "example" {
  name = "example.com"
}

data "pantechdynamics_access_policy_document" "ci" {
  statement {
    sid       = "PushOneRepository"
    effect    = "allow"
    actions   = ["registry:Read", "registry:Pull", "registry:Push"]
    resources = [pantechdynamics_container_repository.api.id]
  }

  statement {
    sid       = "ChangeOneZone"
    effect    = "allow"
    actions   = ["dns:Read", "dns:ChangeRecords"]
    resources = [pantechdynamics_dns_zone.example.id]
  }
}

resource "pantechdynamics_access_policy" "ci" {
  name        = "ci-api-server"
  policy_json = data.pantechdynamics_access_policy_document.ci.json
}

resource "pantechdynamics_access_policy_attachment" "ci" {
  policy_id      = pantechdynamics_access_policy.ci.id
  principal_type = "member"
  principal_id   = "usr_06ggfaabcdefghjkmnpqrstvw" # the ci-bot member
}
