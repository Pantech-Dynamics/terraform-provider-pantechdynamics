# An access key for the application, limited to one bucket. Its secret is
# shown only when the key is created, so it is kept in Terraform state: treat
# the state as a secret. Keys cannot change: changing any argument issues a
# new key.
resource "pantechdynamics_object_storage_key" "app" {
  name    = "app"
  buckets = [pantechdynamics_bucket.uploads.name]
}

# A read-only key for every bucket in the organization.
resource "pantechdynamics_object_storage_key" "reader" {
  name      = "backup-reader"
  read_only = true
}

# AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY for any S3 tool, at endpoint.
output "app_access_key_id" {
  value = pantechdynamics_object_storage_key.app.access_key_id
}

output "app_secret_access_key" {
  value     = pantechdynamics_object_storage_key.app.secret_access_key
  sensitive = true
}
