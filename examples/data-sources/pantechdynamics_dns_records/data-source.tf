# Every MX record set of a zone. Leave type and name out to list them all.
data "pantechdynamics_dns_records" "mail" {
  zone_id = "dnsz_06ggf2abcdefghjkmnpqrstvw"
  type    = "MX"
}

output "mail_servers" {
  value = flatten(data.pantechdynamics_dns_records.mail.records[*].values)
}
