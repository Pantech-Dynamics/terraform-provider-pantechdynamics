---
page_title: "Getting started"
subcategory: ""
description: |-
  Install the provider, connect it to your account with an API key, and run your first read-only configuration.
---

# Getting started

This guide takes you from nothing to a working Terraform setup that talks to your Pantech Dynamics account. Nothing in it creates a resource or costs money.

## What you need

- A current version of [Terraform](https://developer.hashicorp.com/terraform/install). Version 1.11 or later is needed for `pantechdynamics_database`, which uses a write-only password.
- A Pantech Dynamics account.
- An API key, described next.

## Get an API key

An API key is how the provider proves who you are.

1. Sign in to the console. An owner or admin of the account can create keys.
2. Create an API key. Keys start with `PAN_`.
3. Choose its scope:
   - **read** lets the provider look things up. Use it for the examples in this guide.
   - **write** lets the provider create, change and delete resources. It includes read.
4. Copy the key straight away.

Treat the key like a password. Do not put it in a `.tf` file, and do not commit it. A key works only for the API address it was created for, so a key made for one environment gets `401` on another.

## Give the key to Terraform

The simplest way is an environment variable. Run these in the terminal you will run Terraform from:

```shell
export PANTECHDYNAMICS_API_KEY="PAN_your_key_here"
export PANTECHDYNAMICS_BASE_URL="https://api.pantechdynamics.com/public/v1"
```

The base URL must include the `/public/v1` ending. Without it every call fails with `404 GATEWAY_NO_ROUTE`.

You can set both in the provider block instead. If you do, keep the key in a sensitive variable and pass it in from outside:

```terraform
variable "pantechdynamics_api_key" {
  type      = string
  sensitive = true
}

provider "pantechdynamics" {
  base_url = "https://api.pantechdynamics.com/public/v1"
  api_key  = var.pantechdynamics_api_key
}
```

A value set in the provider block wins over the environment variable. There is also an optional `request_timeout` (for example `"90s"`) for how long one request may take. It defaults to 60 seconds.

## Your first configuration

Make a new empty folder and create a file called `main.tf`:

```terraform
terraform {
  required_providers {
    pantechdynamics = {
      source  = "Pantech-Dynamics/pantechdynamics"
      version = "~> 0.1"
    }
  }
}

provider "pantechdynamics" {}

data "pantechdynamics_regions" "all" {}
data "pantechdynamics_plans" "all" {}
data "pantechdynamics_images" "all" {}

output "regions" {
  value = [for r in data.pantechdynamics_regions.all.regions : r.code]
}

output "plans" {
  value = [for p in data.pantechdynamics_plans.all.plans : p.slug]
}

output "images" {
  value = [for i in data.pantechdynamics_images.all.images : i.slug]
}
```

A `data` block only reads. These three read your account's regions, plans and images, which are the names you will need to create a server in the next guide.

## The Terraform loop

Terraform always works in the same four steps.

| Command | What it does |
|---|---|
| `terraform init` | Downloads the provider. Run it once per folder, and again when you change the provider version. |
| `terraform plan` | Shows what would change. Nothing is changed. |
| `terraform apply` | Does it. It shows the plan and asks you to type `yes`. |
| `terraform destroy` | Deletes everything Terraform created in this folder. |

Run them now:

```shell
terraform init
terraform apply
```

You should see `Apply complete! Resources: 0 added, 0 changed, 0 destroyed.` followed by your outputs. That means the provider is installed and your key works.

## Names you will see

Cloud resources are chosen by short stable names, not numbers:

- A **plan** is a size: CPU, memory and disk. Its slug is a short name such as `individual`.
- An **image** is an operating system. Its slug looks like `ubuntu-24-04`.
- A **region** has one or more **zones**. A zone offers either the `standard` placement (a server with its own public address) or the `vpc` placement (a server on a private network of yours).

Always read the valid values from the data sources instead of guessing.

## Where Terraform keeps track

After `apply`, Terraform writes a file called `terraform.tfstate` in your folder. It records what exists. Two rules:

- Never edit it by hand.
- It can contain secrets, such as a generated SSH private key. Do not commit it. For a team, keep state in a remote backend that supports locking.

## Next

- [Your first server](first-server) creates an SSH key, a firewall and a server.
- [Troubleshooting](troubleshooting) explains the errors you might meet.
