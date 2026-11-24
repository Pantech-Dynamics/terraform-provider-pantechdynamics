# Look up an existing object storage bucket by name (or by id) to read its
# endpoint, live storage usage, and tags.
data "pantechdynamics_bucket" "uploads" {
  name = "acme-app-uploads"
}

output "uploads_endpoint" {
  value = data.pantechdynamics_bucket.uploads.endpoint
}

output "uploads_size_bytes" {
  value = data.pantechdynamics_bucket.uploads.size_bytes
}
