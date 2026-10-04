# Instances are imported by id. The API does not report which SSH key an instance
# was created with, so ssh_key_id is not set by an import and is not compared
# afterwards, which means importing never forces a replacement.
terraform import pantechdynamics_instance.web vm_06gg88kvrxt6z7h6js3h1799xm
