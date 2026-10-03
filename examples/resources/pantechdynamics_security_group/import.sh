# Security groups are imported by id. This also works for the account's default
# group, which lets you manage its rules. The default group cannot be deleted,
# so run `terraform state rm` on it instead of destroying it.
terraform import pantechdynamics_security_group.web sg_06gg7164bnszv3pgjbtvg1d0vm
