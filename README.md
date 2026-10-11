# Terraform Provider for Pantech Dynamics

Manage Pantech Dynamics cloud resources with Terraform. The provider talks only to the Pantech Dynamics public REST API.

## Configuration

```hcl
provider "pantechdynamics" {
  base_url = "https://api.pantechdynamics.com/public/v1"
  api_key  = var.pantechdynamics_api_key
}
```

Both settings can come from the environment: `PANTECHDYNAMICS_BASE_URL` and `PANTECHDYNAMICS_API_KEY`.

`pantechdynamics_database` takes its admin password as a write-only argument (`password_wo`), which needs Terraform 1.11 or later. Its `storage_gb` only grows: a smaller value fails at plan time instead of replacing the database. `pantechdynamics_database_snapshot` snapshots a database's data disk, and `private_network = true` on a standard `pantechdynamics_instance` connects it to the private database network (its `private_network_ip` goes in the database's `access_rules` as a `/32`).

An optional `request_timeout` (for example `"90s"`, or `PANTECHDYNAMICS_REQUEST_TIMEOUT`) sets how long one API request may take before it is abandoned. It defaults to 60 seconds. It is separate from the `timeouts` block on each resource, which bounds how long an apply waits for that resource.

## Development

Requires Go 1.27+.

```shell
make build     # go build ./...
make lint      # golangci-lint run
make test      # unit tests
make testacc   # acceptance tests (TF_ACC=1, test zone only)
make generate  # regenerate docs/
```

For local testing, build with `go install` and point `dev_overrides` in `~/.terraformrc` at `$GOPATH/bin`.

## Releasing

Changes add a line under **Unreleased** in [`CHANGELOG.md`](CHANGELOG.md). To
release, rename that heading to the version and date (`## 0.3.0 (2026-11-02)`),
keep an empty `## Unreleased` above it, and merge to `main`.
`.github/workflows/auto-release.yml` sees a version that has no `v` tag yet,
tags it (`v0.3.0`), and runs `release.yml`: tests, then a GPG-signed GoReleaser
release on GitHub, which the Terraform Registry picks up. A push whose newest
version is already tagged releases nothing. Pushing a tag by hand still works.
