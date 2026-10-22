# Every event type a webhook can subscribe to.
data "pantechdynamics_webhook_event_types" "all" {}

# The event types of one resource, for example to subscribe to its failures only.
locals {
  instance_failures = [
    for e in data.pantechdynamics_webhook_event_types.all.event_types : e.type
    if e.resource == "instance" && endswith(e.type, "_failed")
  ]
}

resource "pantechdynamics_webhook" "failures" {
  url    = "https://hooks.example.com/pantech/failures"
  events = local.instance_failures
}
