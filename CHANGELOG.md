# Changelog

## Unreleased

FEATURES:

- **New resource:** `pantechdynamics_database`, a managed PostgreSQL, MySQL or MariaDB database. Create pays for the order first and then waits for provisioning, like an instance. Access rules (the whole CIDR allow-list), attached security groups (`security_group_ids`, with the read-only `effective_access_rules` and `ignored_security_group_rules`) and the power state (`desired_state`) change in place. The admin password is the write-only argument `password_wo` (Terraform 1.11 or later), so it is never stored in plan or state; changing `password_wo_version` sets a new one. Supports import and `timeouts`.
- **New resource:** `pantechdynamics_database_snapshot`, a crash-consistent snapshot of a managed database's data disk. It is created and deleted (any change replaces it), waits for the snapshot to become active, and is imported as `<database_id>/<snapshot_id>`. It outlives its database and stays billed until deleted.
- `pantechdynamics_database`: new `storage_gb` (optional, computed) sets the data disk size at create; increasing it grows the disk in place (`POST /databases/{id}/resize-storage`, online, starting a stopped database for it and stopping it again). A smaller value is an error at plan time, and never replaces the database. A resize in flight is reported as its target size and waited for instead of being sent again.
- `pantechdynamics_instance`: new `private_network` (optional, computed bool) attaches or detaches the interface on the zone's private database network in place, and the read-only `private_network_ip` is its address, to allow on a database as a `/32`. When the security group lets in the private range the API's `SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK` message, which names the rules to narrow, is shown with a hint, on `private_network` for an attach and on `security_group_id` for a group change. It is refused at plan time on an instance in a subnet.
- **New data sources:** `pantechdynamics_database_engines` (engines, version lines and their zones), and the id-or-name lookups `pantechdynamics_security_group`, `pantechdynamics_ssh_key`, `pantechdynamics_instance` and `pantechdynamics_network`.
- `pantechdynamics_public_ip`: new read-only `network_name` and `instance_name`.

BUG FIXES:

- `pantechdynamics_public_ip`: `zone` was always empty, because the API field is `zone`, not `zone_id`.
- Firewall rules and port forwarding rules are read with their single-rule endpoints instead of searching the whole list, and every list (including plans, images, regions and disk offerings) now follows `next_cursor`, so a paginated list can no longer be cut short or fail.
- Rename, stop and resize of instances, resize and delete of volumes, and delete of snapshots are now retried on gateway errors with the same `Idempotency-Key`, which the API replays. SSH key create and delete still never retry, because the API does not replay them.
- Documentation, examples and errors now give the production base URL as `https://api.pantechdynamics.com/public/v1`, and a `GATEWAY_NO_ROUTE` error says to check the `/public/v1` prefix.
