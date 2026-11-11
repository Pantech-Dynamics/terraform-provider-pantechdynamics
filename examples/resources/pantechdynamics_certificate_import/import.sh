# An imported certificate is imported by its id. The PEMs cannot be read
# back: keep them in the configuration. The next apply recognises the same
# certificate by its fingerprint and sends nothing.
terraform import pantechdynamics_certificate_import.shop cert_06ggfcabcdefghjkmnpqrstvw
