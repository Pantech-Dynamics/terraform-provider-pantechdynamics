# Plans priced for a standard VPS (a public IP is included). Use
# placement = "vpc" to price plans for VMs in a VPC instead.
data "pantechdynamics_plans" "all" {}

output "plan_slugs" {
  value = [for p in data.pantechdynamics_plans.all.plans : p.slug]
}

# Look one plan up by its slug.
output "developer_vcpus" {
  value = one([for p in data.pantechdynamics_plans.all.plans : p.vcpu if p.slug == "developer"])
}
