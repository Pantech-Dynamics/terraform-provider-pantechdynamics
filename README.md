# Terraform Provider for Pantech Dynamics

Manage Pantech Dynamics cloud resources with Terraform. The provider talks only to the Pantech Dynamics public REST API.

## Configuration

```hcl
provider "pantechdynamics" {
  base_url = "https://api.pantechdynamics.com/v1"
  api_key  = var.pantechdynamics_api_key
}
```

Both settings can come from the environment: `PANTECHDYNAMICS_BASE_URL` and `PANTECHDYNAMICS_API_KEY`.

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
