# Changelog

## Unreleased

FEATURES:

- **New resource:** `pantechdynamics_database`, a managed PostgreSQL, MySQL or MariaDB database. Create pays for the order first and then waits for provisioning, like an instance. Access rules (the whole CIDR allow-list), attached security groups (`security_group_ids`, with the read-only `effective_access_rules` and `ignored_security_group_rules`) and the power state (`desired_state`) change in place. The admin password is the write-only argument `password_wo` (Terraform 1.11 or later), so it is never stored in plan or state; changing `password_wo_version` sets a new one. Supports import and `timeouts`.
- **New resource:** `pantechdynamics_database_snapshot`, a crash-consistent snapshot of a managed database's data disk. It is created and deleted (any change replaces it), waits for the snapshot to become active, and is imported as `<database_id>/<snapshot_id>`. It outlives its database and stays billed until deleted.
- `pantechdynamics_database`: new `storage_gb` (optional, computed) sets the data disk size at create; increasing it grows the disk in place (`POST /databases/{id}/resize-storage`, online, starting a stopped database for it and stopping it again). A smaller value is an error at plan time, and never replaces the database. A resize in flight is reported as its target size and waited for instead of being sent again.
- `pantechdynamics_instance`: new `private_network` (optional, computed bool) attaches or detaches the interface on the zone's private database network in place, and the read-only `private_network_ip` is its address, to allow on a database as a `/32`. When the security group lets in the private range the API's `SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK` message, which names the rules to narrow, is shown with a hint, on `private_network` for an attach and on `security_group_id` for a group change. It is refused at plan time on an instance in a subnet.
- **New data sources:** `pantechdynamics_database_engines` (engines, version lines and their zones), and the id-or-name lookups `pantechdynamics_security_group`, `pantechdynamics_ssh_key`, `pantechdynamics_instance` and `pantechdynamics_network`.
- `pantechdynamics_public_ip`: new read-only `network_name` and `instance_name`.
- `pantechdynamics_database_engines`: new read-only `storage` on each engine, one entry per zone with `zone_id`, `min_gb`, `min_is_plan_disk`, `max_gb`, `step_gb`, `price_per_gb_month_minor` and `currency` (both null when unpriced): the limits and price for `storage_gb`.
- `pantechdynamics_regions` and `pantechdynamics_region`: new read-only `private_network_cidr` on each placement, the zone's private database network range (null where there is none, and always for `vpc`).
- A failed operation whose failure code is one of the `PROVISIONING_*` codes now adds what to do next (choose another plan or region, apply again later, delete unused resources, or contact support with the operation id). Legacy `PROVISIONING_*` codes on older operations get the hint of the code they map to.
- Database and instance orders that failed with `payment_expired` (not paid within one hour) or, for a database, `organization_deleted` (cancelled because the organization was deleted; any payment returned to credit) say so in the error.

BUG FIXES:

- A `422` error no longer lists every invalid field twice: the API now builds its `detail` from the field errors, so the provider prints the title and the field list instead.
- `pantechdynamics_database` descriptions now match the API's rules exactly: every reserved `admin_username`, `access_rules` host bits and the 1024-range limit shared with `security_group_ids`, `storage_gb`'s zone minimum, and the one-hour payment window.

- `pantechdynamics_public_ip`: `zone` was always empty, because the API field is `zone`, not `zone_id`.
- Firewall rules and port forwarding rules are read with their single-rule endpoints instead of searching the whole list, and every list (including plans, images, regions and disk offerings) now follows `next_cursor`, so a paginated list can no longer be cut short or fail.
- Rename, stop and resize of instances, resize and delete of volumes, and delete of snapshots are now retried on gateway errors with the same `Idempotency-Key`, which the API replays. SSH key create and delete still never retry, because the API does not replay them.
- Documentation, examples and errors now give the production base URL as `https://api.pantechdynamics.com/public/v1`, and a `GATEWAY_NO_ROUTE` error says to check the `/public/v1` prefix.
