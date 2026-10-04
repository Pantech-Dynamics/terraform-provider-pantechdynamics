# A volume is billed by the hour. Look up the offerings first: each has a storage
# type, shared or local, and on staging only local volumes attached to instances.
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

# A customized offering takes its size from size_gb. A fixed offering has its own
# size, and setting a different size_gb is an error.
resource "pantechdynamics_volume" "archive" {
  name               = "archive"
  disk_offering_slug = "custom"
  size_gb            = 100
}

# To grow a volume, change the offering (or size_gb) while it is detached. The
# plan refuses a resize while it is still attached, and a volume never shrinks.
