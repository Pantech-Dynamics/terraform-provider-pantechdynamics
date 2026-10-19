# A volume is billed by the hour. Look up the offerings first: each has a size and a
# storage type. A local volume's disk lives on one host; Pantech moves it to the host
# of the instance you attach it to.
data "pantechdynamics_disk_offerings" "all" {}

# A standalone volume on a fixed offering. The offering sets the size.
resource "pantechdynamics_volume" "scratch" {
  name               = "scratch"
  disk_offering_slug = "small-5gb"
}

# A volume attached to an instance. It is created first and attached in a second
# step. Removing instance_id detaches it: unmount the disk inside the instance
# first, or the data may be damaged. Destroying the volume detaches it for you.
resource "pantechdynamics_volume" "data" {
  name               = "data"
  disk_offering_slug = "small-local-20gb"
  instance_id        = pantechdynamics_instance.web.id

  # A label only. The platform does not mount anything.
  mount_point = "/data"

  timeouts = {
    create = "20m"
    update = "30m"
    delete = "20m"
  }
}

# An encrypted volume. It is created as "requested": the disk is encrypted when the
# Pantech agent formats it inside the instance (see the encrypted volumes guide in the
# documentation), and encryption_status becomes "encrypted" once the platform has
# confirmed it. Changing encrypted replaces the volume. It cannot be combined with
# source_snapshot_id.
resource "pantechdynamics_volume" "secure" {
  name               = "secure"
  disk_offering_slug = "small-local-20gb"
  encrypted          = true
  instance_id        = pantechdynamics_instance.web.id
}

output "secure_volume_encryption" {
  value = pantechdynamics_volume.secure.encryption_status
}

# A customized offering takes its size from size_gb. A fixed offering has its own
# size, and setting a different size_gb is an error.
resource "pantechdynamics_volume" "archive" {
  name               = "archive"
  disk_offering_slug = "custom"
  size_gb            = 100
}

# To grow a volume, change the offering (or size_gb) while it is detached. The
# plan refuses a resize while it is still attached, and a volume never shrinks.

# A volume created from a snapshot. Its size must be at least the snapshot's.
resource "pantechdynamics_volume" "restored" {
  name               = "data-restored"
  disk_offering_slug = "small-local-20gb"
  source_snapshot_id = pantechdynamics_snapshot.data_before_upgrade.id
}
