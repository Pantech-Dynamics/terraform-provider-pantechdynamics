# A token is imported by its id. Its secret cannot be read back, so secret is
# null after an import; replace the token to have Terraform hold a secret.
terraform import pantechdynamics_container_registry_token.ci prt_06ggf6abcdefghjkmnpqrstvw
