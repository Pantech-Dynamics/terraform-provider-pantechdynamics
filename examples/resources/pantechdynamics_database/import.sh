# Databases are imported by id. The password is never readable, so it is not
# imported: password_wo stays required in the configuration, and setting
# password_wo_version after an import sets the password to password_wo. The API
# reports the plan by id only, so plan_slug is not compared after an import.
terraform import pantechdynamics_database.orders db_7k2q9x4m1a
