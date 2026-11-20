# A key is imported by its id. Its secret cannot be read back, so
# secret_access_key is null after an import; replace the key to have
# Terraform hold a secret.
terraform import pantechdynamics_object_storage_key.app osk_06ggf6abcdefghjkmnpqrstvw
