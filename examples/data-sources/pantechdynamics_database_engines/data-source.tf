# Engines and the version lines offered for new databases, with their zones.
data "pantechdynamics_database_engines" "all" {}

output "postgresql_versions" {
  value = [
    for v in one([for e in data.pantechdynamics_database_engines.all.engines : e if e.engine == "postgresql"]).versions : v.version
  ]
}

# Each zone's data disk limits for storage_gb, and the price per GB-month.
output "postgresql_storage" {
  value = {
    for s in one([for e in data.pantechdynamics_database_engines.all.engines : e if e.engine == "postgresql"]).storage :
    s.zone_id => { max_gb = s.max_gb, step_gb = s.step_gb, price_per_gb_month_minor = s.price_per_gb_month_minor }
  }
}
