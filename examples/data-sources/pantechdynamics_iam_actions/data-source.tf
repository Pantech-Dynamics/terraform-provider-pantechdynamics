# Every DNS action a statement can name.
data "pantechdynamics_iam_actions" "dns" {
  service = "dns"
}

output "dns_actions" {
  value = data.pantechdynamics_iam_actions.dns.actions[*].action
}
