# Take a snapshot of a volume automatically. Times are in UTC. Only successful
# snapshots count toward retention, and older ones are deleted.
resource "pantechdynamics_snapshot_schedule" "data" {
  volume_id       = pantechdynamics_volume.data.id
  frequency       = "daily"
  retention_count = 7 # 1 to 168, defaults to 7
}

# The same for an instance's root disk. Set enabled = false to pause it.
resource "pantechdynamics_snapshot_schedule" "web" {
  instance_id = pantechdynamics_instance.web.id
  frequency   = "weekly"
  enabled     = true
}

# The same for a database's data disk. Each copy is crash-consistent.
resource "pantechdynamics_snapshot_schedule" "app_db" {
  database_id     = pantechdynamics_database.app.id
  frequency       = "daily"
  retention_count = 14
}

# The API cannot delete a schedule, so destroying one of these pauses it
# (enabled = false) and leaves it on the platform. The snapshots a schedule makes
# are not managed by Terraform, and they are billed for storage like any other.
