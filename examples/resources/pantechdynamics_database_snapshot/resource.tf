# A snapshot of a database's data disk, taken with the engine running. It is
# crash-consistent only (as after a power cut), not an engine-level backup: set
# the database's desired_state = "stopped" first for a quiesced copy. It is
# billed by size for every started hour until deleted, and it outlives its
# database. Restoring a database from a snapshot is not available yet.
resource "pantechdynamics_database_snapshot" "orders_pre_upgrade" {
  database_id = pantechdynamics_database.orders.id

  # Unique for the database. Changing it takes a new snapshot and deletes this one.
  name = "pre-upgrade"

  timeouts = {
    create = "30m"
    delete = "30m"
  }
}

output "orders_snapshot_size_bytes" {
  value = pantechdynamics_database_snapshot.orders_pre_upgrade.size_bytes
}
