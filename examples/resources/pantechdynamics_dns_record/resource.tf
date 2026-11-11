resource "pantechdynamics_dns_zone" "example" {
  name = "example.com"
}

# Two A records for www. Values are a set: their order does not matter.
resource "pantechdynamics_dns_record" "www" {
  zone_id = pantechdynamics_dns_zone.example.id
  name    = "www"
  type    = "A"
  ttl     = 300
  values  = ["203.0.113.10", "203.0.113.11"]
}

# Mail for the zone apex ("@"). Host names may be written with or without the
# trailing dot; the plan does not show a change either way.
resource "pantechdynamics_dns_record" "mx" {
  zone_id = pantechdynamics_dns_zone.example.id
  name    = "@"
  type    = "MX"
  values  = ["10 mail.example.com", "20 backup-mail.example.com"]
}

# A long TXT value is split into 255-byte strings for you.
resource "pantechdynamics_dns_record" "spf" {
  zone_id = pantechdynamics_dns_zone.example.id
  name    = "@"
  type    = "TXT"
  values  = ["v=spf1 include:_spf.example.com -all"]
  comment = "Mail senders"
}

# A private address needs allow_private, for a name only VPC hosts resolve.
resource "pantechdynamics_dns_record" "db" {
  zone_id       = pantechdynamics_dns_zone.example.id
  name          = "db.internal"
  type          = "A"
  values        = ["10.0.1.20"]
  allow_private = true
}
