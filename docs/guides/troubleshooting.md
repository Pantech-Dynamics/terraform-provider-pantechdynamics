---
page_title: "Troubleshooting"
subcategory: ""
description: |-
  What common provider errors mean and how to fix them.
---

# Troubleshooting

Every API error includes a `request_id`. Keep it: support can find the exact request from it.

## Errors from the API

| You see | It means | What to do |
|---|---|---|
| `401 UNAUTHENTICATED` | The key is wrong, revoked, expired, or made for a different environment. | Check `PANTECHDYNAMICS_API_KEY` and that the base URL matches the environment the key was created for. |
| `403 INSUFFICIENT_SCOPE` or `FORBIDDEN` | The key is read-only, or the action is not allowed. | Create a key with the **write** scope. |
| `404 GATEWAY_NO_ROUTE` | The base URL is wrong. This is not a missing resource. | Use `https://api.pantechdynamics.com/public/v1`, including `/public/v1`. |
| `402 INSUFFICIENT_CREDIT` | The account cannot pay for the order. | Add credit or a default card, then apply again. Nothing was ordered. |
| `409` with a name in the message | A resource with that name already exists. SSH key names are unique per account. | Choose another name, or import the existing one. |
| `409 SSH_KEY_ALREADY_EXISTS` | The same public key is already registered under another name. | Import that key, or remove the old one. |
| `422 VALIDATION_FAILED` | A value was rejected. The message lists each field and its problem. | Fix the fields named in the error. |
| `429` | Too many requests. | The provider waits and retries. If it keeps happening, lower the number of parallel operations with `terraform apply -parallelism=2`. |

## Problems you may notice

**It is slow.** Some calls take more than a minute, and creating or deleting a server takes minutes. Raise `request_timeout` in the provider block, and the `timeouts` block on the resource, if you hit a timeout. The two are separate: `request_timeout` bounds one request, and `timeouts` bounds how long an apply waits for a resource.

**A create failed halfway.** Run `terraform apply` again. Terraform saves what it already created. For servers and databases the provider also checks for a leftover object with the same name before ordering again.

**A delete says the resource is in use.** A security group cannot be deleted while a server uses it, and a volume cannot be deleted while it is attached. Terraform normally orders this correctly. If you removed only one resource from the configuration, make sure what depended on it is removed or changed first.

**A name cannot be reused right after deleting.** The names of deleted security groups and snapshots stay reserved for a while. Wait a few minutes or choose a new name.

**A stop or start did not run.** The provider never sends a start to a running server or a stop to a stopped one, because the platform fails that operation. Check `desired_state` against the server's `observed_state`.

**A failed operation shows a failure code.** A code starting with `PROVISIONING_` comes with the next step to take. If it says to contact support, quote the operation id from the message.

**`terraform plan` wants to replace something unexpectedly.** Read the line that says why. Some arguments cannot change in place, such as a server's image or a key's public key. Replacing deletes the old object, so check the plan before you confirm.

## Getting help

Contact support with:

- the `request_id` or operation id from the error,
- the resource type and name,
- the output of `terraform version`.

Never include your API key or your state file.
