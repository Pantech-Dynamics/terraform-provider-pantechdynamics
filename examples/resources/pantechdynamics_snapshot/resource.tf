# A snapshot is a point-in-time copy of a volume or of an instance's root disk, and
# its storage is billed monthly by size. Supported snapshot targets:
#   - a local volume attached to an instance
#   - the root disk of a stopped instance

# A snapshot of a volume that is attached to an instance.
resource "pantechdynamics_snapshot" "data_before_upgrade" {
  name      = "before-upgrade"
  volume_id = pantechdynamics_volume.data.id
}

# A snapshot of an instance's root disk. Stop the instance first.
resource "pantechdynamics_snapshot" "web_root" {
  name        = "web-root-1"
  instance_id = pantechdynamics_instance.web.id

  timeouts = {
    create = "30m"
    delete = "20m"
  }
}

# A snapshot cannot be changed: any change replaces it. To create a volume from
# a snapshot, set source_snapshot_id on a pantechdynamics_volume.
