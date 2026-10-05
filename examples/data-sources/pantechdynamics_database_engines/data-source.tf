# Engines and the version lines offered for new databases, with their zones.
data "pantechdynamics_database_engines" "all" {}

output "postgresql_versions" {
  value = [
    for v in one([for e in data.pantechdynamics_database_engines.all.engines : e if e.engine == "postgresql"]).versions : v.version
  ]
}
