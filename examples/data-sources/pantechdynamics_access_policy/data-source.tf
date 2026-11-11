# Look a policy up by id or name, including Pantech's managed ones.
data "pantechdynamics_access_policy" "read_only" {
  id = "pol_pantech_read_only"
}

output "read_only_statements" {
  value = data.pantechdynamics_access_policy.read_only.policy_json
}
