# Look one plan up by slug. A wrong slug fails the plan with the list of valid ones.
data "pantechdynamics_plan" "small" {
  slug = "individual"

  # placement = "vpc" # Plans and prices differ for instances in a VPC.
}

output "upfront_cost_minor" {
  value = data.pantechdynamics_plan.small.initial_payment_minor
}
