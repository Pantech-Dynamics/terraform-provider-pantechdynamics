# A bucket for application uploads, limited to 100 GiB, keeping earlier
# versions of objects, with one prefix anyone may read. Storage is billed per
# GiB stored, measured hourly. Object storage is in closed beta.
resource "pantechdynamics_bucket" "uploads" {
  name            = "acme-app-uploads"
  quota_bytes     = 100 * 1024 * 1024 * 1024
  versioning      = true
  public_prefixes = ["public/"]

  # Destroying a bucket that holds objects fails unless this is true, which
  # deletes the objects with it.
  force_destroy = false
}

output "uploads_endpoint" {
  value = pantechdynamics_bucket.uploads.endpoint
}
