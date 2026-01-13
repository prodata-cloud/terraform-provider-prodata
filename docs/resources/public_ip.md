---
page_title: "prodata_public_ip Resource - ProData Provider"
description: |-
  Manages a ProData public IP address.
---

# prodata_public_ip (Resource)

Manages a ProData public IP address.

~> **Note:** Only the `name` attribute can be updated in-place. Changing `region` or `project_tag` will force the creation of a new public IP (destroy and recreate).

## Example Usage

```terraform
resource "prodata_public_ip" "example" {
  name = "my-public-ip"
}
```

## Schema

### Required

- `name` (String) The name of the public IP. **This is the only attribute that can be updated in-place.**

### Optional

- `region` (String) Region where the public IP will be created. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag where the public IP will be created. If not specified, uses the provider's default project_tag. Changing this forces a new resource.

### Read-Only

- `id` (Number) The unique identifier of the public IP.
- `ip` (String) The allocated public IP address.
- `mask` (String) The subnet mask of the public IP (e.g., /24).
- `gateway` (String) The gateway IP address.

## Import

Public IPs cannot be imported as the API does not provide sufficient information to reconstruct the Terraform state.
