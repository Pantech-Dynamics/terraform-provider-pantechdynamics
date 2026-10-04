data "pantechdynamics_regions" "all" {}

# Zones where a standard VPS can be created right now.
output "available_standard_zones" {
  value = flatten([
    for r in data.pantechdynamics_regions.all.regions : [
      for p in r.placements : p.zone if p.kind == "standard" && p.available
    ]
  ])
}
