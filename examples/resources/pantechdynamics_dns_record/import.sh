# A record set is imported as "<zone_id>/<record_id>", or by name and type as
# "<zone_id>/<name>/<type>" ("@" is the zone apex).
terraform import pantechdynamics_dns_record.www dnsz_06ggf2abcdefghjkmnpqrstvw/dnsr_06ggf3abcdefghjkmnpqrstvw
terraform import pantechdynamics_dns_record.mx dnsz_06ggf2abcdefghjkmnpqrstvw/@/MX
