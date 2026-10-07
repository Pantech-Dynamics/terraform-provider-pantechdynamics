---
page_title: "Your first server"
subcategory: ""
description: |-
  Create an SSH key, a firewall and a server with Terraform, change it, import an existing one, and clean up.
---

# Your first server

This guide creates a real server. **A server costs money.** Creating one places an order that is paid up front from your account credit or your default card, and a running server is billed while it exists. Read the whole guide before you apply, and run `terraform destroy` when you are done.

You need a **write** API key and the setup from [Getting started](getting-started).

## What you will build

```
SSH key  ──┐
           ├──►  Server
Firewall ──┘
```

- an **SSH key**, so you can log in,
- a **security group**, which is the firewall: inbound traffic that no rule allows is dropped,
- a **server** that uses both.

Terraform sees that the server refers to the key and the firewall, so it creates those first and deletes them last. You never have to order the steps yourself.

## Step 1: find a plan and an image

Use the data sources from the first guide to list valid names:

```shell
terraform output plans
terraform output images
```

Pick a plan slug (the smallest is the cheapest) and an image slug. To see what a plan costs before ordering it, look it up. A wrong slug fails the plan and lists the valid ones:

```terraform
data "pantechdynamics_plan" "small" {
  slug = "individual"
}

output "upfront_cost_minor" {
  value = data.pantechdynamics_plan.small.initial_payment_minor
}
```

Prices are in minor units of your account currency and are estimates, not quotes.

## Step 2: write the configuration

Create `server.tf` next to your `main.tf`:

```terraform
resource "pantechdynamics_ssh_key" "admin" {
  name       = "admin"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "pantechdynamics_security_group" "web" {
  name = "web-tier"

  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "203.0.113.0/24" },
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "0.0.0.0/0" },
    { direction = "egress", protocol = "all", cidr = "0.0.0.0/0" },
  ]
}

resource "pantechdynamics_instance" "web" {
  name              = "web-1"
  plan_slug         = "individual"
  image_slug        = "ubuntu-24-04"
  ssh_key_id        = pantechdynamics_ssh_key.admin.id
  security_group_id = pantechdynamics_security_group.web.id
}

# A standard server reports its address as private_ipv4. A server in a VPC subnet
# has only a private address, so it needs a public IP to be reached from outside.
output "web_address" {
  value = pantechdynamics_instance.web.private_ipv4
}
```

Things to change:

- **`public_key`:** the path to your own public key. If you leave `public_key` out, Pantech Dynamics generates a pair. The private key is then shown once and kept only in your Terraform state, so protect the state.
- **`cidr = "203.0.113.0/24"`:** replace it with your own network. `0.0.0.0/0` means the whole internet, which is fine for a web port and a bad idea for SSH.
- **`name`:** it becomes the server's hostname: 1 to 63 letters, digits or hyphens, not starting or ending with a hyphen. Keep it unique. The provider refuses a duplicate name before ordering, because the platform would accept the order and then fail it.
- **`plan_slug` and `image_slug`:** use values from Step 1.

The rules are replaced as a whole, so always list every rule you want, and a change affects every server that uses the group. A security group needs at least one rule.

## Step 3: plan, then apply

```shell
terraform plan
```

Read the plan. It should say `3 to add`. Nothing has been created or charged yet.

```shell
terraform apply
```

Type `yes` to confirm. Creating the server takes a minute or more. The provider waits for the order to be paid and for the server to reach `running`, then prints the address.

If something fails midway, run `terraform apply` again. Terraform keeps a record of what it already created and carries on from there.

## Step 4: log in

```shell
ssh your_user@$(terraform output -raw web_address)
```

The user name depends on the image. If you cannot connect, check that your firewall rule for port 22 allows the address you are connecting from.

## Changing a server

Edit the configuration and run `terraform plan` again. Terraform tells you whether a change happens in place or replaces the server.

| You change | What happens |
|---|---|
| `name` | Renamed in place, in seconds. |
| `desired_state` to `"stopped"` or back to `"running"` | Stops or starts in place. A stopped server is billed for storage only. |
| `plan_slug` to a **bigger** plan | Resized in place. The server is stopped and restarted, it takes several minutes, and the disk grows. |
| `plan_slug` to a **smaller** plan | Refused by the platform. Use `terraform apply -replace=pantechdynamics_instance.web`, which destroys the disk. |
| `security_group_id` | Updated in place. The server is stopped, switched, and started again if it should be running. |
| `image_slug`, `region`, `tags` | Replaces the server. |

Always read the plan before you type `yes`. A line that says `must be replaced` means the old server and its data are deleted.

## Bring an existing server under Terraform

If a server already exists, you can adopt it instead of creating a new one. Write the resource block first, then import by id:

```shell
terraform import pantechdynamics_instance.web vm_your_server_id
terraform plan
```

Aim for a plan that says `No changes`. Import works the same way for SSH keys (`sshk_...`) and security groups (`sg_...`). The private key of a generated SSH pair can never be imported, because the API does not return it again.

## If someone deletes it outside Terraform

Run `terraform plan`. The provider reads the real state, sees the server is gone, and plans to create it again. This is called drift, and Terraform fixes it on the next apply.

## Clean up

```shell
terraform destroy
```

Terraform deletes the server first and the firewall and key after it. Deleting a server takes a few minutes. When it finishes, check the console to confirm nothing is left.

A deleted security group's name stays reserved for a while. If you destroy and recreate at once and get an error about the name, wait a few minutes or use a new name.

## Next

- Resource reference pages for volumes, snapshots, networks, subnets, public IPs and managed databases are in the sidebar.
- [Troubleshooting](troubleshooting) lists common errors and fixes.
